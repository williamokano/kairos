package engine_test

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/engine"
	"github.com/williamokano/kairos/internal/eventstore"
	"github.com/williamokano/kairos/internal/executor/local"
)

// newSourceRepoDir returns a temp dir guaranteed NOT to share a parent
// with t.TempDir()'s own tree — checkSafeSource (internal/workspace,
// L06) refuses a source repo living inside "kairos's own state
// directory," defined as workRoot's parent; since t.TempDir() nests every
// call from one test under the same per-test parent, a workRoot and a
// source repo both built via t.TempDir() would collide with that check
// even though they're unrelated. os.MkdirTemp("", ...) roots in a
// separate top-level bucket, avoiding the false positive.
func newSourceRepoDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "kairos-fork-src-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// initGitRepo creates a real git repository at dir with one committed
// file, for tests that need a real workspace: write source.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "log.txt"), []byte("line0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "initial")
}

// forkTestEngine is newTestEngine plus workspace configuration — a real
// executor is required (git subprocess spawning, not exectest.Fake).
func forkTestEngine(t *testing.T, workRoot, sourceRepo string) (*engine.Engine, eventstore.Store) {
	t.Helper()
	st := openStore(t)
	eng := engine.New(engine.Config{
		Store:         st,
		Executor:      local.New(local.DefaultBootIDProvider()),
		BootID:        local.DefaultBootIDProvider(),
		WorkRoot:      workRoot,
		WorkspaceRepo: sourceRepo,
		KillGrace:     200 * time.Millisecond,
		Logger:        slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	})
	return eng, st
}

// twoWriteNodeDef is a two-node workspace: write workflow — each node
// appends one line to log.txt and records how many lines it saw, so a
// fork's restored workspace content is independently verifiable (not
// just "a workspace exists").
func twoWriteNodeDef(t *testing.T) string {
	t.Helper()
	defPath := filepath.Join(t.TempDir(), "def.yaml")
	yaml := `
name: fork-e2e
nodes:
  - id: n1
    actor: shell
    workspace: write
    prompt: |
      echo line1 >> log.txt
      echo '{"lines":2}' > "$KAIROS_OUTPUT_PATH"
    output: { lines: "int!" }
  - id: n2
    actor: shell
    workspace: write
    prompt: |
      echo line2 >> log.txt
      echo '{"lines":3}' > "$KAIROS_OUTPUT_PATH"
    output: { lines: "int!" }
`
	if err := os.WriteFile(defPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}
	return defPath
}

func twoNodeGraph() domain.Graph {
	return domain.Graph{
		Entry: "n1",
		Nodes: []domain.Node{
			{ID: "n1", Retry: domain.RetryPolicy{MaxAttempts: 1}, LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 1}},
			{ID: "n2", Retry: domain.RetryPolicy{MaxAttempts: 1}, LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 1}},
		},
		Edges: map[domain.NodeID]map[domain.EdgeTrigger]domain.NodeID{
			"n1": {domain.OnSuccess: "n2", domain.OnFailure: domain.SinkFail, domain.OnTimeout: domain.SinkFail},
			"n2": {domain.OnSuccess: domain.SinkSucceed, domain.OnFailure: domain.SinkFail, domain.OnTimeout: domain.SinkFail},
		},
	}
}

func runToTerminal(t *testing.T, ctx context.Context, st interface {
	GetRunState(context.Context, string) (domain.RunState, bool, error)
}, runID string, deadline time.Duration) domain.RunState {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		state, ok, err := st.GetRunState(ctx, runID)
		if err != nil {
			t.Fatalf("GetRunState: %v", err)
		}
		if ok && state.Status.Terminal() {
			return state
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("run %s did not reach a terminal state within %s", runID, deadline)
	return domain.RunState{}
}

func TestEngine_forkCopiesReasoningExactlyAndRestoresWorkspaceApproximately(t *testing.T) {
	sourceRepo := newSourceRepoDir(t)
	initGitRepo(t, sourceRepo)

	workRoot := t.TempDir()
	eng, st := forkTestEngine(t, workRoot, sourceRepo)
	defPath := twoWriteNodeDef(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := eng.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = eng.Stop(context.Background()) }()

	runID := "run_forkorig"
	meta := appendMetaFor(runID)
	if _, err := st.AppendIf(ctx, runID, 0, []domain.Event{
		domain.TriggerReceived{RunID: runID, TriggerRef: "test", DefinitionRef: defPath, CorrelationID: runID},
	}, meta); err != nil {
		t.Fatalf("append trigger: %v", err)
	}
	if _, err := st.AppendIf(ctx, runID, 1, []domain.Event{
		domain.RunStarted{RunID: runID, Graph: twoNodeGraph()},
	}, meta); err != nil {
		t.Fatalf("append run started: %v", err)
	}

	final := runToTerminal(t, ctx, st, runID, 15*time.Second)
	if final.Status != domain.RunSucceeded {
		t.Fatalf("original run Status = %s, want succeeded; state=%+v", final.Status, final)
	}

	// Find the sequence of the FIRST node's snapshot (post-n1, pre-n2) —
	// that's the fork point: reasoning includes only n1's completion,
	// the workspace should show exactly "line1" appended, not "line2".
	envs, err := st.Read(ctx, runID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var forkAtSeq int
	seenN1Output := false
	for _, env := range envs {
		if out, ok := env.Event.(domain.NodeOutputReceived); ok && out.NodeID == "n1" {
			seenN1Output = true
		}
		if snap, ok := env.Event.(domain.WorkspaceSnapshotTaken); ok && snap.NodeID == "n1" && seenN1Output {
			forkAtSeq = env.Sequence
			break
		}
	}
	if forkAtSeq == 0 {
		t.Fatal("no workspace.snapshot.taken recorded for n1 — the node-boundary snapshot hook did not fire")
	}

	result, err := eng.Fork(ctx, engine.ForkRequest{FromRunID: runID, AtSequence: forkAtSeq})
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if result.Drifted {
		t.Fatal("expected an exact-match fork (no drift) at a recorded snapshot sequence")
	}

	// Reasoning restored exactly: the new run's copied prefix must fold
	// to a state where n1 already succeeded and n2 has not yet started —
	// i.e. it must continue on its own from n2, not redo n1.
	newState, ok, err := st.GetRunState(ctx, result.NewRunID)
	if err != nil {
		t.Fatalf("GetRunState(new): %v", err)
	}
	if !ok {
		t.Fatalf("forked run %s: no state recorded", result.NewRunID)
	}
	if n1execs := newState.Executions["n1"]; len(n1execs) != 1 || n1execs[0].Status != domain.ExecSucceeded {
		t.Fatalf("forked run's n1 = %+v, want exactly one Succeeded exec (copied, not redone)", n1execs)
	}

	finalNew := runToTerminal(t, ctx, st, result.NewRunID, 15*time.Second)
	if finalNew.Status != domain.RunSucceeded {
		t.Fatalf("forked run Status = %s, want succeeded; state=%+v", finalNew.Status, finalNew)
	}

	// n1 must have run exactly ONCE across the fork's whole lifetime — a
	// re-dispatch bug would show two NodeExecutionStarted{n1} events.
	newEnvs, err := st.Read(ctx, result.NewRunID)
	if err != nil {
		t.Fatalf("Read(new): %v", err)
	}
	n1Starts := 0
	for _, env := range newEnvs {
		if s, ok := env.Event.(domain.NodeExecutionStarted); ok && s.NodeID == "n1" {
			n1Starts++
		}
	}
	if n1Starts != 1 {
		t.Errorf("n1 started %d times in the forked run's own stream, want exactly 1 (the copied historical record, never redispatched)", n1Starts)
	}

	// Workspace approximately restored: forked run's log.txt has line1
	// but not line2, at the moment of fork — then n2 appends its own
	// line2 as the fork continues.
	forkedWorkspace := filepath.Join(workRoot, result.NewRunID, "repo", "log.txt")
	body, err := os.ReadFile(forkedWorkspace)
	if err != nil {
		t.Fatalf("reading forked workspace log.txt: %v", err)
	}
	got := string(body)
	if !strings.Contains(got, "line1") {
		t.Errorf("forked workspace missing line1: %q", got)
	}
	if !strings.Contains(got, "line2") {
		// n2 ran again on the fork and appended its own line2 — expected.
		t.Errorf("forked workspace missing line2 after n2 re-ran on the fork: %q", got)
	}

	// run.forked recorded with the right lineage.
	sawForked := false
	for _, env := range newEnvs {
		if rf, ok := env.Event.(domain.RunForked); ok {
			sawForked = true
			if rf.FromRunID != runID {
				t.Errorf("RunForked.FromRunID = %q, want %q", rf.FromRunID, runID)
			}
			if rf.LineageRoot != runID {
				t.Errorf("RunForked.LineageRoot = %q, want %q (never-forked ancestor)", rf.LineageRoot, runID)
			}
		}
	}
	if !sawForked {
		t.Error("expected run.forked recorded on the new run's stream")
	}
}

func TestEngine_forkRefusesDriftByDefault(t *testing.T) {
	sourceRepo := newSourceRepoDir(t)
	initGitRepo(t, sourceRepo)
	workRoot := t.TempDir()
	eng, st := forkTestEngine(t, workRoot, sourceRepo)
	defPath := twoWriteNodeDef(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := eng.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = eng.Stop(context.Background()) }()

	runID := "run_driftorig"
	meta := appendMetaFor(runID)
	if _, err := st.AppendIf(ctx, runID, 0, []domain.Event{
		domain.TriggerReceived{RunID: runID, TriggerRef: "test", DefinitionRef: defPath, CorrelationID: runID},
	}, meta); err != nil {
		t.Fatalf("append trigger: %v", err)
	}
	if _, err := st.AppendIf(ctx, runID, 1, []domain.Event{
		domain.RunStarted{RunID: runID, Graph: twoNodeGraph()},
	}, meta); err != nil {
		t.Fatalf("append run started: %v", err)
	}
	final := runToTerminal(t, ctx, st, runID, 15*time.Second)
	if final.Status != domain.RunSucceeded {
		t.Fatalf("original run Status = %s, want succeeded", final.Status)
	}

	// Sequence 2 is early enough (right after RunStarted / before any
	// node-boundary snapshot exists yet) that no exact snapshot exists
	// there — the refusal path.
	_, err := eng.Fork(ctx, engine.ForkRequest{FromRunID: runID, AtSequence: 2})
	if err == nil {
		t.Fatal("expected Fork to refuse without --allow-drift")
	}
	if err != engine.ErrWorkspaceDrift {
		t.Fatalf("Fork error = %v, want ErrWorkspaceDrift", err)
	}

	result, err := eng.Fork(ctx, engine.ForkRequest{FromRunID: runID, AtSequence: 2, AllowDrift: true})
	if err != nil {
		t.Fatalf("Fork with AllowDrift: %v", err)
	}
	if !result.Drifted {
		t.Error("expected Drifted=true when forking past a missing snapshot with AllowDrift")
	}

	newEnvs, err := st.Read(ctx, result.NewRunID)
	if err != nil {
		t.Fatalf("Read(new): %v", err)
	}
	sawDrift := false
	for _, env := range newEnvs {
		if d, ok := env.Event.(domain.ForkWorkspaceDrifted); ok {
			sawDrift = true
			if d.RequestedSeq != 2 {
				t.Errorf("RequestedSeq = %d, want 2", d.RequestedSeq)
			}
		}
	}
	if !sawDrift {
		t.Error("expected fork.workspace.drifted recorded on the new run")
	}
}
