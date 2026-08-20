package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/eventstore"
	"github.com/williamokano/kairos/internal/executor/local"
)

// TestEngine_failingCommandGateLoopsThenParksUnderLoopGuard drives a real
// shell node whose one declared command gate always fails through the
// engine's real advance loop end-to-end: 05-gates.md's invariant
// (schema-valid output -> gates in declared order -> edges) routes the
// rejection back to the SAME node for another attempt via domain's
// existing LoopGuard machinery (advanceNodeGatesEvaluated,
// internal/domain/advance.go), never a parallel retry system L10 built.
// Bounded by limits.loopGuard.maxIterationsPerNode: 2, the node must park
// with ParkLoopGuardExceeded rather than loop forever.
func TestEngine_failingCommandGateLoopsThenParksUnderLoopGuard(t *testing.T) {
	st := openStore(t)
	workRoot := t.TempDir()
	runID := "run_gate_loop"

	defPath := filepath.Join(t.TempDir(), "def.yaml")
	yaml := `
name: gate-loop
limits:
  loopGuard: { maxIterationsPerNode: 2, onExceeded: escalate-to-human }
gates:
  always-fails:
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
    gates: [always-fails]
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
			{ID: "n1", Retry: domain.RetryPolicy{MaxAttempts: 1}, LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 2}},
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

	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		state, ok, err := st.GetRunState(ctx, runID)
		if err != nil {
			t.Fatalf("GetRunState: %v", err)
		}
		if ok {
			execs := state.Executions["n1"]
			if len(execs) > 0 {
				last := execs[len(execs)-1]
				if last.Status == domain.ExecParked {
					if last.ParkReason != domain.ParkLoopGuardExceeded {
						t.Fatalf("ParkReason = %q, want %q", last.ParkReason, domain.ParkLoopGuardExceeded)
					}
					if len(last.Findings) == 0 {
						t.Fatal("expected findings to survive onto the parked execution")
					}
					if last.Iteration != 2 {
						t.Fatalf("Iteration = %d, want 2 (loop guard bound)", last.Iteration)
					}
					assertConstraintEvaluatedRecordedForEveryAttempt(t, ctx, st, runID)
					return
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("node did not park under the loop guard within the deadline")
}

// assertConstraintEvaluatedRecordedForEveryAttempt confirms
// constraint.evaluated was appended once per attempt (never skipped, per
// 05-gates.md's "fake the result" defence) and every recorded evaluation
// failed — this gate can never pass.
func assertConstraintEvaluatedRecordedForEveryAttempt(t *testing.T, ctx context.Context, st eventstore.Store, runID string) {
	t.Helper()
	envs, err := st.Read(ctx, runID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var evaluated int
	for _, env := range envs {
		ce, ok := env.Event.(domain.ConstraintEvaluated)
		if !ok {
			continue
		}
		evaluated++
		if ce.Passed {
			t.Errorf("constraint.evaluated{GateID: %q} recorded Passed=true, want false (always-fails gate)", ce.GateID)
		}
		if ce.GateID != "always-fails" {
			t.Errorf("GateID = %q, want %q", ce.GateID, "always-fails")
		}
	}
	if evaluated != 2 {
		t.Errorf("constraint.evaluated recorded %d times, want 2 (one per attempt)", evaluated)
	}
}
