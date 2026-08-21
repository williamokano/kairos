package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/engine"
	"github.com/williamokano/kairos/internal/executor/local"
)

// slowShellDefPath is a one-node shell workflow (no workspace: write, so no
// git repo fixture is needed) whose command sleeps long enough that a test
// can reliably observe and cancel it mid-flight.
func slowShellDefPath(t *testing.T) string {
	t.Helper()
	defPath := filepath.Join(t.TempDir(), "slow-shell.yaml")
	const yaml = `
name: cancel-e2e
nodes:
  - id: n1
    actor: shell
    prompt: |
      sleep 5
      echo '{"ok":true}' > "$KAIROS_OUTPUT_PATH"
    output: { ok: "bool!" }
`
	if err := os.WriteFile(defPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}
	return defPath
}

func TestCancel_reasonRequired(t *testing.T) {
	st := openStore(t)
	eng := newTestEngine(t, st, local.New(local.DefaultBootIDProvider()), local.DefaultBootIDProvider(), t.TempDir())
	if err := eng.Cancel(context.Background(), "run_x", ""); err != engine.ErrCancelReasonRequired {
		t.Fatalf("err = %v, want ErrCancelReasonRequired", err)
	}
}

func TestCancel_rejectsAlreadyTerminalRun(t *testing.T) {
	st := openStore(t)
	workRoot := t.TempDir()
	runID := "run_cancel_terminal"
	defPath := filepath.Join(t.TempDir(), "def.yaml")
	if err := os.WriteFile(defPath, []byte(`
name: cancel-terminal
nodes:
  - id: n1
    actor: shell
    prompt: echo '{"ok":true}' > "$KAIROS_OUTPUT_PATH"
    output: { ok: "bool!" }
`), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}

	eng := newTestEngine(t, st, local.New(local.DefaultBootIDProvider()), local.DefaultBootIDProvider(), workRoot)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := eng.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = eng.Stop(context.Background()) }()

	meta := appendMetaFor(runID)
	if _, err := st.AppendIf(ctx, runID, 0, []domain.Event{
		domain.TriggerReceived{RunID: runID, TriggerRef: "test", DefinitionRef: defPath, CorrelationID: runID},
	}, meta); err != nil {
		t.Fatalf("append trigger: %v", err)
	}
	if _, err := st.AppendIf(ctx, runID, 1, []domain.Event{
		domain.RunStarted{RunID: runID, Graph: oneNodeGraph()},
	}, meta); err != nil {
		t.Fatalf("append run started: %v", err)
	}

	final := runToTerminal(t, ctx, st, runID, 10*time.Second)
	if final.Status != domain.RunSucceeded {
		t.Fatalf("setup: run Status = %s, want succeeded", final.Status)
	}

	err := eng.Cancel(ctx, runID, "changed my mind")
	if err == nil {
		t.Fatal("Cancel on an already-terminal run: want an error, got nil")
	}
}

func TestCancel_rejectsPendingRun(t *testing.T) {
	st := openStore(t)
	eng := newTestEngine(t, st, local.New(local.DefaultBootIDProvider()), local.DefaultBootIDProvider(), t.TempDir())
	ctx := context.Background()
	runID := "run_cancel_pending"
	meta := appendMetaFor(runID)
	if _, err := st.AppendIf(ctx, runID, 0, []domain.Event{
		domain.TriggerReceived{RunID: runID, TriggerRef: "test", DefinitionRef: "unused.yaml", CorrelationID: runID},
	}, meta); err != nil {
		t.Fatalf("append trigger: %v", err)
	}

	err := eng.Cancel(ctx, runID, "too early")
	if err == nil {
		t.Fatal("Cancel on a Pending run: want an error, got nil")
	}
}

// TestCancel_stopsARunningNodeAndReachesCancelled is the genuine
// end-to-end proof: a real shell node sleeping 5s is dispatched, Cancel is
// called while it is still Executing, and the run must (a) record
// NodeExecutionInterrupted BEFORE the process is killed (dispatchSignalNode's
// own documented ordering) and (b) reach RunCancelledS — not hang, not
// silently stay Executing forever.
func TestCancel_stopsARunningNodeAndReachesCancelled(t *testing.T) {
	st := openStore(t)
	workRoot := t.TempDir()
	runID := "run_cancel_live"
	defPath := slowShellDefPath(t)

	eng := newTestEngine(t, st, local.New(local.DefaultBootIDProvider()), local.DefaultBootIDProvider(), workRoot)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := eng.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = eng.Stop(context.Background()) }()

	meta := appendMetaFor(runID)
	if _, err := st.AppendIf(ctx, runID, 0, []domain.Event{
		domain.TriggerReceived{RunID: runID, TriggerRef: "test", DefinitionRef: defPath, CorrelationID: runID},
	}, meta); err != nil {
		t.Fatalf("append trigger: %v", err)
	}
	if _, err := st.AppendIf(ctx, runID, 1, []domain.Event{
		domain.RunStarted{RunID: runID, Graph: oneNodeGraph()},
	}, meta); err != nil {
		t.Fatalf("append run started: %v", err)
	}

	// Wait for the node to genuinely be Executing before cancelling — a
	// cancel raced against dispatch itself is a different (untested) shape.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		state, ok, err := st.GetRunState(ctx, runID)
		if err != nil {
			t.Fatalf("GetRunState: %v", err)
		}
		if ok {
			if execs, ok := state.Executions["n1"]; ok && len(execs) > 0 && execs[len(execs)-1].Status == domain.ExecExecuting {
				goto executing
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("node never reached ExecExecuting")
executing:

	if err := eng.Cancel(ctx, runID, "no longer needed"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	final := runToTerminal(t, ctx, st, runID, 10*time.Second)
	if final.Status != domain.RunCancelledS {
		t.Fatalf("final Status = %s, want cancelled", final.Status)
	}

	// RunState.Status flips to Cancelled the instant advanceRunCancelled
	// folds — n1's own NodeExecutionInterrupted is a SEPARATE event,
	// appended and folded by dispatchSignalNode a beat later (the CmdSignalNode
	// domain.Advance returned), so poll for it independently rather than
	// asserting it's already visible in the same GetRunState read that
	// first observed the terminal run status.
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		state, ok, err := st.GetRunState(ctx, runID)
		if err != nil {
			t.Fatalf("GetRunState: %v", err)
		}
		if ok {
			if execs, ok := state.Executions["n1"]; ok && len(execs) > 0 && execs[len(execs)-1].Status == domain.ExecInterrupted {
				goto interrupted
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("n1 never recorded node.execution.interrupted after Cancel")
interrupted:

	// A second Cancel on the now-terminal run must be rejected, not silently
	// accepted or double-appended.
	if err := eng.Cancel(ctx, runID, "again"); err == nil {
		t.Fatal("Cancel on an already-cancelled run: want an error, got nil")
	}
}
