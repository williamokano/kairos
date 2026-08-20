package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/executor/local"
)

// TestEngine_pauseHoldsAtTheNextBoundaryWithoutInterruptingWhatsRunning
// proves L19's park mode is genuinely different from Stop: a node that is
// ALREADY executing when SetPaused(true) is called must be left to finish
// naturally — no NodeExecutionInterrupted, no kill — while the NEXT node
// in the graph must NOT start until Resume.
func TestEngine_pauseHoldsAtTheNextBoundaryWithoutInterruptingWhatsRunning(t *testing.T) {
	st := openStore(t)
	workRoot := t.TempDir()
	runID := "run_pause"

	defPath := filepath.Join(t.TempDir(), "def.yaml")
	readyFile := filepath.Join(t.TempDir(), "ready")
	goFile := filepath.Join(t.TempDir(), "go")
	yaml := `
name: pause-e2e
nodes:
  - id: n1
    actor: shell
    prompt: |
      touch "` + readyFile + `"
      while [ ! -f "` + goFile + `" ]; do sleep 0.05; done
      echo '{"ok":true}' > "$KAIROS_OUTPUT_PATH"
    output: { ok: "bool!" }
  - id: n2
    actor: rule
    output: { x: "string" }
`
	if err := os.WriteFile(defPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}

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

	graph := domain.Graph{
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

	// Wait for n1 to genuinely be running (it touched readyFile), THEN
	// pause — this is the case that matters: pausing while something is
	// already Executing.
	waitForFile(t, readyFile, 5*time.Second)
	eng.SetPaused(ctx, true)
	if !eng.Paused() {
		t.Fatal("expected Paused() to report true")
	}

	// Let n1 finish now.
	if err := os.WriteFile(goFile, []byte("go"), 0o600); err != nil {
		t.Fatalf("writing go file: %v", err)
	}

	// n1 must reach Succeeded (it was NOT interrupted), but the run must
	// NOT reach a terminal state — n2 must be held, not dispatched.
	deadline := time.Now().Add(5 * time.Second)
	var n1Succeeded bool
	for time.Now().Before(deadline) {
		state, ok, err := st.GetRunState(ctx, runID)
		if err != nil {
			t.Fatalf("GetRunState: %v", err)
		}
		if ok {
			if execs := state.Executions["n1"]; len(execs) > 0 && execs[len(execs)-1].Status == domain.ExecSucceeded {
				n1Succeeded = true
			}
			if state.Status.Terminal() {
				t.Fatalf("run reached terminal state %s while paused — n2 should have been held", state.Status)
			}
		}
		if n1Succeeded {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !n1Succeeded {
		t.Fatal("n1 never reached Succeeded — pause must not interrupt work already in flight")
	}

	// Give the (correctly) paused engine a moment to prove it stays
	// parked — n2's NodeExecution row exists the instant n1's routing
	// decision folds (domain.Advance's dispatchExec creates it as Pending
	// unconditionally, before the engine ever gets a chance to dispatch
	// it — see internal/domain/advance.go), so the real assertion is that
	// its status never advances past Pending while paused, not that the
	// row is absent.
	time.Sleep(200 * time.Millisecond)
	state, ok, err := st.GetRunState(ctx, runID)
	if err != nil || !ok {
		t.Fatalf("GetRunState: ok=%v err=%v", ok, err)
	}
	if execs := state.Executions["n2"]; len(execs) > 0 && execs[len(execs)-1].Status != domain.ExecPending {
		t.Fatalf("n2 status = %s while paused, want Pending — pause must hold at the next node boundary", execs[len(execs)-1].Status)
	}

	// Resume: n2 (a rule actor, near-instant) should now run and the run
	// should reach Succeeded.
	eng.SetPaused(ctx, false)
	if eng.Paused() {
		t.Fatal("expected Paused() to report false after resume")
	}

	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		state, ok, err := st.GetRunState(ctx, runID)
		if err != nil {
			t.Fatalf("GetRunState: %v", err)
		}
		if ok && state.Status.Terminal() {
			if state.Status != domain.RunSucceeded {
				t.Fatalf("run Status = %s, want %s", state.Status, domain.RunSucceeded)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("run did not reach Succeeded after resume within the deadline")
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
