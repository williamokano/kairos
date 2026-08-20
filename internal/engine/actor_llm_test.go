package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/engine"
	"github.com/williamokano/kairos/internal/eventstore"
	"github.com/williamokano/kairos/internal/executor/local"
)

// writeFakeLLM writes a shell script standing in for a real LLM CLI
// (claude/codex/gemini) — never a real network call in this suite. It
// reads and discards stdin (the prompt, per the file contract: prompt on
// stdin, never argv), then behaves however script says. Executable, so it
// can be set directly as engine.Config.LLMBinary (Argv is [binary] alone
// — no shell wrapper, matching the real CLI-invocation shape).
func writeFakeLLM(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-llm.sh")
	full := "#!/bin/sh\ncat >/dev/null\n" + script + "\n"
	if err := os.WriteFile(path, []byte(full), 0o700); err != nil {
		t.Fatalf("writing fake LLM script: %v", err)
	}
	return path
}

func newTestEngineWithLLM(t *testing.T, st eventstore.Store, workRoot, llmBinary string) *engine.Engine {
	t.Helper()
	return engine.New(engine.Config{
		Store:     st,
		Executor:  local.New(local.DefaultBootIDProvider()),
		BootID:    local.DefaultBootIDProvider(),
		WorkRoot:  workRoot,
		LLMBinary: llmBinary,
		KillGrace: 200 * time.Millisecond,
	})
}

func writeAndStartRun(t *testing.T, st eventstore.Store, workRoot, runID, yaml string) {
	t.Helper()
	defPath := filepath.Join(t.TempDir(), "def.yaml")
	if err := os.WriteFile(defPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}

	graph := domain.Graph{
		Entry: "n1",
		Nodes: []domain.Node{
			{ID: "n1", Retry: domain.RetryPolicy{MaxAttempts: 2}, LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 1}},
		},
		Edges: map[domain.NodeID]map[domain.EdgeTrigger]domain.NodeID{
			"n1": {domain.OnSuccess: domain.SinkSucceed, domain.OnFailure: domain.SinkFail, domain.OnTimeout: domain.SinkFail},
		},
	}

	ctx := context.Background()
	meta := appendMetaFor(runID)
	if _, err := st.AppendIf(ctx, runID, 0, []domain.Event{
		domain.TriggerReceived{RunID: runID, TriggerRef: "test", DefinitionRef: defPath, CorrelationID: runID},
	}, meta); err != nil {
		t.Fatalf("append trigger: %v", err)
	}
	if _, err := st.AppendIf(ctx, runID, 1, []domain.Event{
		domain.RunStarted{RunID: runID, Graph: graph},
	}, meta); err != nil {
		t.Fatalf("append run started: %v", err)
	}
}

func waitForTerminal(t *testing.T, ctx context.Context, st eventstore.Store, runID string, timeout time.Duration) domain.RunStatus {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		state, ok, err := st.GetRunState(ctx, runID)
		if err != nil {
			t.Fatalf("GetRunState: %v", err)
		}
		if ok && state.Status.Terminal() {
			return state.Status
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("run did not reach a terminal state within the deadline")
	return ""
}

// TestEngine_llmActorRunsFakeCLIToSuccess is the llm-actor happy path: a
// single-node run whose actor is "claude", driven by a fake CLI that
// writes valid output.json on its first invocation — proving the file
// contract (KAIROS_OUTPUT/KAIROS_SCHEMA env, prompt on stdin) end to end
// through a real subprocess.
func TestEngine_llmActorRunsFakeCLIToSuccess(t *testing.T) {
	st := openStore(t)
	workRoot := t.TempDir()
	runID := "run_llm_happy"

	fake := writeFakeLLM(t, `echo '{"ok":true}' > "$KAIROS_OUTPUT"`)
	eng := newTestEngineWithLLM(t, st, workRoot, fake)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := eng.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = eng.Stop(context.Background()) }()

	writeAndStartRun(t, st, workRoot, runID, `
name: e2e-llm
nodes:
  - id: n1
    actor: claude
    output: { ok: "bool!" }
`)

	if got := waitForTerminal(t, ctx, st, runID, 8*time.Second); got != domain.RunSucceeded {
		t.Fatalf("run Status = %s, want %s", got, domain.RunSucceeded)
	}

	envs, err := st.Read(ctx, runID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	sawSession := false
	for _, env := range envs {
		if _, ok := env.Event.(domain.LLMSessionStarted); ok {
			sawSession = true
		}
	}
	if !sawSession {
		t.Error("expected llm.session.started to be recorded")
	}
}

// TestEngine_llmActorRepairsInvalidOutputThenSucceeds proves 04-agents.md's
// Stage 2 repair turn: the fake CLI writes schema-invalid output on its
// first invocation, then valid output once a marker file (left by its own
// first run, inside KAIROS_DIR — stable across the repair turn since the
// repair turn reuses the same exec's dir) exists — exactly the shape one
// bounded in-session repair attempt takes.
func TestEngine_llmActorRepairsInvalidOutputThenSucceeds(t *testing.T) {
	st := openStore(t)
	workRoot := t.TempDir()
	runID := "run_llm_repair"

	fake := writeFakeLLM(t, `
marker="$KAIROS_DIR/.invoked"
if [ -f "$marker" ]; then
  echo '{"ok":true}' > "$KAIROS_OUTPUT"
else
  touch "$marker"
  echo '{"ok":"not-a-bool"}' > "$KAIROS_OUTPUT"
fi
`)
	eng := newTestEngineWithLLM(t, st, workRoot, fake)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := eng.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = eng.Stop(context.Background()) }()

	writeAndStartRun(t, st, workRoot, runID, `
name: e2e-llm-repair
nodes:
  - id: n1
    actor: claude
    retry:
      maxAttempts: 1
    output: { ok: "bool!" }
`)

	if got := waitForTerminal(t, ctx, st, runID, 8*time.Second); got != domain.RunSucceeded {
		t.Fatalf("run Status = %s, want %s (the repair turn should have fixed the output within the SAME attempt)", got, domain.RunSucceeded)
	}

	envs, err := st.Read(ctx, runID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	repairs, starts := 0, 0
	for _, env := range envs {
		switch env.Event.(type) {
		case domain.OutputRepairAttempted:
			repairs++
		case domain.NodeExecutionStarted:
			starts++
		}
	}
	if repairs != 1 {
		t.Errorf("output.repair.attempted count = %d, want 1", repairs)
	}
	if starts != 1 {
		t.Errorf("node.execution.started count = %d, want 1 (the repair turn must NOT be a new top-level attempt)", starts)
	}
}

// TestEngine_llmActorSessionResumeFailsAcrossAttemptsWithoutAWorkspace
// proves 04-agents.md's path-keying trap is detected rather than silently
// hit: a non-workspace node's scratch dir is per-exec (workRoot/runID/
// execID), so it necessarily differs between attempt 1 and attempt 2 — a
// resume attempt would find nothing. The engine must notice BEFORE trying
// to resume (session.resume.failed) and mint a fresh session instead,
// never silently proceeding as if resume worked.
func TestEngine_llmActorSessionResumeFailsAcrossAttemptsWithoutAWorkspace(t *testing.T) {
	st := openStore(t)
	workRoot := t.TempDir()
	runID := "run_llm_resume_trap"

	// Always fails, forcing domain's retry ladder to dispatch attempt 2.
	fake := writeFakeLLM(t, `exit 1`)
	eng := newTestEngineWithLLM(t, st, workRoot, fake)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := eng.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = eng.Stop(context.Background()) }()

	writeAndStartRun(t, st, workRoot, runID, `
name: e2e-llm-resume-trap
nodes:
  - id: n1
    actor: claude
    sessionAffinity: node
    retry:
      maxAttempts: 2
    output: { ok: "bool!" }
`)

	if got := waitForTerminal(t, ctx, st, runID, 8*time.Second); got != domain.RunFailed {
		t.Fatalf("run Status = %s, want %s (both attempts fail unconditionally)", got, domain.RunFailed)
	}

	envs, err := st.Read(ctx, runID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	sessions, resumeFailed := 0, 0
	for _, env := range envs {
		switch e := env.Event.(type) {
		case domain.LLMSessionStarted:
			sessions++
			if e.Resumed {
				t.Errorf("session %d: Resumed = true, want false (resume should never have been attempted)", sessions)
			}
		case domain.SessionResumeFailed:
			resumeFailed++
		}
	}
	if sessions != 2 {
		t.Errorf("llm.session.started count = %d, want 2 (one per attempt)", sessions)
	}
	if resumeFailed != 1 {
		t.Errorf("session.resume.failed count = %d, want 1 (attempt 2 tried and failed to resume attempt 1's session)", resumeFailed)
	}
}
