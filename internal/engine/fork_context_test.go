package engine_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/engine"
	"github.com/williamokano/kairos/internal/executor/local"
)

// slowSingleNodeDefPath is a one-node workspace: write workflow whose
// shell command sleeps briefly before completing — long enough that a
// caller context cancelled the instant Fork returns is guaranteed to have
// already fired before the node's background reaper tries to record its
// outcome, deterministically exercising the race a real HTTP request's
// context creates (see fork.go's dispatchCtx doc comment).
func slowSingleNodeDefPath(t *testing.T) string {
	t.Helper()
	defPath := filepath.Join(t.TempDir(), "slow.yaml")
	const yaml = `
name: fork-ctx-e2e
nodes:
  - id: n1
    actor: shell
    workspace: write
    prompt: |
      sleep 0.2
      echo line1 >> log.txt
      echo '{"ok":true}' > "$KAIROS_OUTPUT_PATH"
    output: { ok: "bool!" }
`
	if err := os.WriteFile(defPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}
	return defPath
}

// TestEngine_forkSurvivesCallerContextCancelledRightAfterItReturns is a
// real, previously-latent bug this document's own end-to-end web UI test
// found (cmd/kairos/diff_compare_webui_test.go): Engine.Fork dispatches
// its continuation Cmds synchronously using the CALLER's ctx — fine when
// called from a long-lived test context, but POST /runs/{id}/fork's own
// ctx is an *http.Request's context, cancelled the instant the handler
// returns, well before a real dispatched node's background reaper
// (reapShell's e.wg-tracked watcher) finishes waiting for the child
// process and tries to record its outcome. Every subsequent
// e.store.AppendIf that watcher attempted then failed with
// context.Canceled — silently, since reapShell only checks that error to
// decide whether to snapshot next, never logging it — leaving the node
// Executing forever with no recorded outcome at all, even though the
// process had genuinely completed. This test reproduces the exact shape:
// cancel the caller's ctx immediately after Fork returns, then prove the
// forked run still reaches a terminal state anyway (context.WithoutCancel
// is the fix, in fork.go).
func TestEngine_forkSurvivesCallerContextCancelledRightAfterItReturns(t *testing.T) {
	sourceRepo := newSourceRepoDir(t)
	initGitRepo(t, sourceRepo)
	workRoot := t.TempDir()
	st := openStore(t)
	eng := engine.New(engine.Config{
		Store: st, Executor: local.New(local.DefaultBootIDProvider()),
		BootID: local.DefaultBootIDProvider(), WorkRoot: workRoot,
		WorkspaceRepo: sourceRepo, KillGrace: 200 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	})

	bgCtx, bgCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer bgCancel()
	if _, err := eng.Reconcile(bgCtx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := eng.Start(bgCtx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = eng.Stop(context.Background()) }()

	runID := "run_forkctx_orig"
	meta := appendMetaFor(runID)
	defPath := slowSingleNodeDefPath(t)
	if _, err := st.AppendIf(bgCtx, runID, 0, []domain.Event{
		domain.TriggerReceived{RunID: runID, TriggerRef: "test", DefinitionRef: defPath, CorrelationID: runID},
	}, meta); err != nil {
		t.Fatalf("append trigger: %v", err)
	}
	if _, err := st.AppendIf(bgCtx, runID, 1, []domain.Event{
		domain.RunStarted{RunID: runID, Graph: oneNodeGraph()},
	}, meta); err != nil {
		t.Fatalf("append run started: %v", err)
	}
	if final := runToTerminal(t, bgCtx, st, runID, 15*time.Second); final.Status != domain.RunSucceeded {
		t.Fatalf("original run Status = %s, want succeeded", final.Status)
	}

	// Fork at run.started's own sequence (2: trigger.received,
	// run.started) — before n1 ever dispatched, so Fork must
	// synchronously re-dispatch n1 for real in the restored workspace,
	// exactly the code path that spawns reapShell's background watcher.
	callerCtx, callerCancel := context.WithCancel(context.Background())
	result, err := eng.Fork(callerCtx, engine.ForkRequest{FromRunID: runID, AtSequence: 2, AllowDrift: true})
	// Simulate an *http.Request's context ending the instant the
	// handler returns — before n1's 0.2s sleep has any chance to finish.
	callerCancel()
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}

	final := runToTerminal(t, context.Background(), st, result.NewRunID, 10*time.Second)
	if final.Status != domain.RunSucceeded {
		t.Fatalf("forked run Status = %s, want succeeded — a caller context cancelled right after "+
			"Fork returns must not silently strand the node it dispatched", final.Status)
	}
}
