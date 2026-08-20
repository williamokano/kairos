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

// TestEngine_waivableFalseGateFailureIsNeverSilentlyPassed proves
// 05-gates.md's invariant directly: there is no code path in this engine
// that can turn a failed, waivable: false gate's verdict back into
// Passed=true — the field exists today only as a declared invariant
// (there is no waiver.grant mechanism at all yet, L11 scope), so this
// test is really asserting "the absence of a bypass," which is the
// correct thing to assert about a control with no override mechanism.
func TestEngine_waivableFalseGateFailureIsNeverSilentlyPassed(t *testing.T) {
	st := openStore(t)
	workRoot := t.TempDir()
	runID := "run_waivable_false"

	defPath := filepath.Join(t.TempDir(), "def.yaml")
	yaml := `
name: waivable-false
limits:
  loopGuard: { maxIterationsPerNode: 1, onExceeded: escalate-to-human }
gates:
  guardrails:
    kind: command
    waivable: false
    check:
      command: ["false"]
      expect: { exitCode: 0 }
nodes:
  - id: n1
    actor: shell
    prompt: "echo '{\"ok\":true}' > \"$KAIROS_OUTPUT_PATH\""
    output: { ok: "bool!" }
    gates: [guardrails]
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

	// With loopGuard.maxIterationsPerNode: 1, iteration 1's failure hits
	// the bound immediately: the node must park, never succeed.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		state, ok, err := st.GetRunState(ctx, runID)
		if err != nil {
			t.Fatalf("GetRunState: %v", err)
		}
		if ok {
			execs := state.Executions["n1"]
			if len(execs) > 0 {
				last := execs[len(execs)-1]
				if last.Status == domain.ExecSucceeded {
					t.Fatal("n1 reached ExecSucceeded — a waivable: false, always-failing gate was somehow bypassed")
				}
				if state.Status == domain.RunSucceeded {
					t.Fatal("run reached RunSucceeded — a waivable: false, always-failing gate was somehow bypassed")
				}
				if last.Status == domain.ExecParked {
					if last.ParkReason != domain.ParkLoopGuardExceeded {
						t.Fatalf("ParkReason = %q, want %q", last.ParkReason, domain.ParkLoopGuardExceeded)
					}
					return
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("node did not reach a terminal/parked state within the deadline")
}
