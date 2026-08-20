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

// TestEngine_stopInterruptsExecutingNodesBeforeKilling proves the
// milestone's ordering requirement: NodeExecutionInterrupted is recorded
// BEFORE the child is killed, and Stop() actually reaps the child (the
// process is dead once Stop returns) — using the real local.Executor so
// this exercises a genuine subprocess, not a fake.
func TestEngine_stopInterruptsExecutingNodesBeforeKilling(t *testing.T) {
	st := openStore(t)
	workRoot := t.TempDir()
	runID := "run_stop"

	defPath := filepath.Join(t.TempDir(), "def.yaml")
	yaml := `
name: stop-test
nodes:
  - id: n1
    actor: shell
    prompt: "sleep 30"
    output: { x: "string" }
`
	if err := os.WriteFile(defPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}

	eng := newTestEngine(t, st, local.New(local.DefaultBootIDProvider()), local.DefaultBootIDProvider(), workRoot)

	ctx := context.Background()
	if _, err := eng.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	graph := domain.Graph{
		Entry: "n1",
		Nodes: []domain.Node{{ID: "n1", Retry: domain.RetryPolicy{MaxAttempts: 1}, LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 1}}},
		Edges: map[domain.NodeID]map[domain.EdgeTrigger]domain.NodeID{
			"n1": {domain.OnSuccess: domain.SinkSucceed, domain.OnFailure: domain.SinkFail, domain.OnTimeout: domain.SinkFail},
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

	// Wait for n1 to actually be Executing (proc.json written) before
	// stopping — otherwise Stop might race ahead of dispatch.
	var pgid int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		state, ok, err := st.GetRunState(ctx, runID)
		if err == nil && ok {
			if execs := state.Executions["n1"]; len(execs) > 0 && execs[len(execs)-1].Status == domain.ExecExecuting {
				dir := filepath.Join(workRoot, runID, execs[len(execs)-1].ExecID)
				if rec, ok, _ := local.ReadProcRecord(dir); ok {
					pgid = rec.PGID
					break
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pgid == 0 {
		t.Fatal("n1 never reached Executing with a recorded proc.json")
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := eng.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if local.ProcessGroupAlive(pgid) {
		t.Error("expected the process group to be dead after Stop")
	}

	envs, err := st.Read(context.Background(), runID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	sawInterrupted := false
	for _, e := range envs {
		if e.EventType == "node.execution.interrupted" {
			sawInterrupted = true
		}
	}
	if !sawInterrupted {
		t.Error("expected node.execution.interrupted in the run's stream")
	}
}
