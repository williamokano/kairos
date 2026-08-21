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

// TestEngine_preStartFailureNeverProducesAnIllegalTransition is NL-31's
// enforcing test: a failure discovered BEFORE a process is ever spawned
// (here, a workspace: write node with no WorkspaceRepo configured) must
// still route through startThenFail's NodeExecutionStarted-then-Failed
// sequence, never appendNodeFailed directly against a still-Pending
// exec — which internal/domain's legalExecEvents table rejects outright
// (only Executing accepts NodeExecutionFailed; ExecPending accepts only
// NodeExecutionStarted). Before this fix, the run got stuck forever:
// dispatchShellActor's early return produced ErrIllegalTransition, which
// the engine only logs, leaving the exec permanently Pending and the run
// permanently Running with no recorded failure at all.
func TestEngine_preStartFailureNeverProducesAnIllegalTransition(t *testing.T) {
	st := openStore(t)
	workRoot := t.TempDir()
	runID := "run_prestart_fail"

	defPath := filepath.Join(t.TempDir(), "def.yaml")
	yaml := `
name: prestart-fail
nodes:
  - id: n1
    actor: shell
    workspace: write
    prompt: "echo '{\"ok\":true}' > \"$KAIROS_OUTPUT_PATH\""
    output: { ok: "bool!" }
`
	if err := os.WriteFile(defPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}

	// Deliberately no WorkspaceRepo: dispatchShellActor's workspace:
	// write branch fails before local.Executor.Start is ever called —
	// the exec never leaves Pending under the old, buggy code path.
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

	graph := domain.Graph{
		Entry: "n1",
		Nodes: []domain.Node{
			{ID: "n1", Retry: domain.RetryPolicy{MaxAttempts: 1}, LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 1}},
		},
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

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		state, ok, err := st.GetRunState(ctx, runID)
		if err != nil {
			t.Fatalf("GetRunState: %v", err)
		}
		if ok && state.Status.Terminal() {
			if state.Status != domain.RunFailed {
				t.Fatalf("run Status = %s, want %s; state=%+v", state.Status, domain.RunFailed, state)
			}
			execs := state.Executions["n1"]
			if len(execs) != 1 {
				t.Fatalf("want exactly one exec for n1, got %d: %+v", len(execs), execs)
			}
			if execs[0].Status != domain.ExecFailed {
				t.Fatalf("exec status = %s, want %s", execs[0].Status, domain.ExecFailed)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("run did not reach a terminal state within the deadline — the pre-start failure was likely swallowed as an illegal transition, leaving the exec stuck Pending forever")
}
