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

// TestEngine_passingExprGateRoutesToSuccess is the expr-kind counterpart
// to TestEngine_failingCommandGateLoopsThenParksUnderLoopGuard: a real
// shell node whose typed output makes its declared expr gate true must
// route straight to success, with the gate's constraint.evaluated fact
// recorded honestly.
func TestEngine_passingExprGateRoutesToSuccess(t *testing.T) {
	st := openStore(t)
	workRoot := t.TempDir()
	runID := "run_expr_pass"

	defPath := filepath.Join(t.TempDir(), "def.yaml")
	yaml := `
name: expr-pass
gates:
  has-summary:
    kind: expr
    check: { expr: "output.summary != \"\"" }
nodes:
  - id: n1
    actor: shell
    prompt: "echo '{\"summary\":\"done\"}' > \"$KAIROS_OUTPUT_PATH\""
    output: { summary: "string!" }
    gates: [has-summary]
`
	if err := os.WriteFile(defPath, []byte(yaml), 0o600); err != nil {
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
			if state.Status != domain.RunSucceeded {
				t.Fatalf("run Status = %s, want %s; state=%+v", state.Status, domain.RunSucceeded, state)
			}
			envs, err := st.Read(ctx, runID)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			var sawEvaluated bool
			for _, env := range envs {
				if ce, ok := env.Event.(domain.ConstraintEvaluated); ok {
					sawEvaluated = true
					if !ce.Passed {
						t.Errorf("constraint.evaluated{GateID: %q}.Passed = false, want true", ce.GateID)
					}
				}
			}
			if !sawEvaluated {
				t.Error("expected at least one constraint.evaluated event")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("run did not reach a terminal state within the deadline")
}
