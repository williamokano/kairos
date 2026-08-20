package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/eventstore"
	"github.com/williamokano/kairos/internal/executor/exectest"
)

// TestEngine_nonIdempotentNodeParksAfterRestart: a node with no
// sideEffectFree declaration resolves to restartPolicy: fail-to-human
// (the default per 12-build-plan.md). When reconciliation finds it Lost
// at boot, domain.Advance still computes the normal retry (a fresh
// NodeExecution, Pending, with a CmdStartNode) — but the engine
// deliberately leaves it undispatched rather than auto-retrying, since a
// non-idempotent node's outcome is unknown and re-running it costs real
// side effects. This is the reachable, testable meaning of "parks after
// restart" for L05 (registry.RestartPolicy governs dispatch; domain has
// no direct Lost-to-Parked transition — see reconcile.go's
// recoverLost doc comment).
func TestEngine_nonIdempotentNodeParksAfterRestart(t *testing.T) {
	st := openStore(t)
	workRoot := t.TempDir()
	runID := "run_park"
	defPath := writeMilestoneLikeDefinition(t, workRoot) // sideEffectFree unset -> fail-to-human

	ctx := context.Background()
	meta := appendMetaFor(runID)
	if _, err := st.AppendIf(ctx, runID, 0, []domain.Event{
		domain.TriggerReceived{RunID: runID, TriggerRef: "test", DefinitionRef: defPath, CorrelationID: runID},
	}, meta); err != nil {
		t.Fatalf("append trigger: %v", err)
	}
	// MaxAttempts: 2 so Lost's fold computes a real retry Cmd (attempt 1
	// < MaxAttempts 2) rather than exhausting straight to $fail — the
	// scenario worth gating on RestartPolicy.
	graph := domain.Graph{
		Entry: "n1",
		Nodes: []domain.Node{{ID: "n1", Retry: domain.RetryPolicy{MaxAttempts: 2}, LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 1}}},
		Edges: map[domain.NodeID]map[domain.EdgeTrigger]domain.NodeID{
			"n1": {domain.OnSuccess: domain.SinkSucceed, domain.OnFailure: domain.SinkFail, domain.OnTimeout: domain.SinkFail},
		},
	}
	if _, err := st.AppendIf(ctx, runID, 1, []domain.Event{
		domain.RunStarted{RunID: runID, Graph: graph},
	}, meta); err != nil {
		t.Fatalf("append run started: %v", err)
	}
	execID := "n1#a1.i1"
	if _, err := st.AppendIf(ctx, runID, 2, []domain.Event{
		domain.NodeExecutionStarted{RunID: runID, NodeID: "n1", ExecID: execID, Attempt: 1, Iteration: 1},
	}, meta); err != nil {
		t.Fatalf("append node execution started: %v", err)
	}

	// No proc.json — crashed before Start confirmed anything.
	dir := filepath.Join(workRoot, runID, execID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	eng := newTestEngine(t, st, exectest.NewFake(), fakeBootID{id: "boot-1"}, workRoot)
	report, err := eng.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if report.Lost != 1 {
		t.Errorf("report.Lost = %d, want 1", report.Lost)
	}

	envs, err := st.Read(ctx, runID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	sawLost, sawSecondStart := false, false
	for _, e := range envs {
		switch e.EventType {
		case "node.execution.lost":
			sawLost = true
		case "node.execution.started":
			if s, ok := e.Event.(domain.NodeExecutionStarted); ok && s.Attempt == 2 {
				sawSecondStart = true
			}
		}
	}
	if !sawLost {
		t.Error("expected node.execution.lost in the run's stream")
	}
	if sawSecondStart {
		t.Error("expected NO attempt-2 node.execution.started — fail-to-human must not auto-retry")
	}

	state, ok, err := st.GetRunState(ctx, runID)
	if err != nil {
		t.Fatalf("GetRunState: %v", err)
	}
	if !ok {
		t.Fatal("expected run state to exist")
	}
	if state.Status != domain.RunRunning {
		t.Errorf("run Status = %s, want %s (parked, not progressed and not failed)", state.Status, domain.RunRunning)
	}
	execs := state.Executions["n1"]
	if len(execs) != 2 {
		t.Fatalf("Executions[n1] = %v, want 2 rows (lost attempt 1 + the undispatched pending attempt 2 domain.Advance still created)", execs)
	}
	if execs[1].Status != domain.ExecPending {
		t.Errorf("attempt 2 Status = %s, want %s (created by the fold, never dispatched)", execs[1].Status, domain.ExecPending)
	}
}

func appendMetaFor(runID string) eventstore.AppendMeta {
	return eventstore.AppendMeta{Actor: "test", CorrelationID: runID, OccurredAt: time.Unix(0, 0)}
}
