package domain

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

const testRunID = "run_1"

func startedRun(t *testing.T, g Graph) RunState {
	t.Helper()
	state, _, err := Advance(RunState{}, TriggerReceived{RunID: testRunID}, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("TriggerReceived: %v", err)
	}
	state, cmds, err := Advance(state, RunStarted{RunID: testRunID, Graph: g}, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("RunStarted: %v", err)
	}
	if len(cmds) == 0 {
		t.Fatal("expected RunStarted to dispatch the entry node")
	}
	return state
}

func TestAdvance_triggerReceivedInitialisesAPendingRun(t *testing.T) {
	state, cmds, err := Advance(RunState{}, TriggerReceived{RunID: testRunID}, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if state.Status != RunPending {
		t.Errorf("Status = %s, want %s", state.Status, RunPending)
	}
	if cmds != nil {
		t.Errorf("expected no cmds from TriggerReceived, got %v", cmds)
	}
}

func TestAdvance_runStartedDispatchesTheEntryNodeAndEntersRunning(t *testing.T) {
	g := singleNodeGraph("n1")
	state, cmds, err := Advance(RunState{ID: testRunID, Status: RunPending, Executions: map[NodeID][]NodeExecution{}}, RunStarted{RunID: testRunID, Graph: g}, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if state.Status != RunRunning {
		t.Errorf("Status = %s, want %s", state.Status, RunRunning)
	}
	if len(cmds) != 1 {
		t.Fatalf("cmds = %v, want exactly one CmdStartNode", cmds)
	}
	start, ok := cmds[0].(CmdStartNode)
	if !ok {
		t.Fatalf("cmds[0] = %T, want CmdStartNode", cmds[0])
	}
	if start.NodeID != "n1" || start.Attempt != 1 || start.Iteration != 1 {
		t.Errorf("CmdStartNode = %+v, want NodeID=n1 Attempt=1 Iteration=1", start)
	}
	exec, ok := state.current("n1")
	if !ok || exec.Status != ExecPending {
		t.Errorf("expected node n1 to have a Pending NodeExecution, got %+v (ok=%v)", exec, ok)
	}
}

func TestAdvance_nodeExecutionStartedMovesPendingToExecuting(t *testing.T) {
	state := startedRun(t, singleNodeGraph("n1"))
	exec, _ := state.current("n1")

	state, cmds, err := Advance(state, NodeExecutionStarted{RunID: testRunID, NodeID: "n1", ExecID: exec.ExecID, Attempt: 1, Iteration: 1}, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if cmds != nil {
		t.Errorf("expected no cmds, got %v", cmds)
	}
	got, _ := state.current("n1")
	if got.Status != ExecExecuting {
		t.Errorf("Status = %s, want %s", got.Status, ExecExecuting)
	}
}

func TestAdvance_schemaInvalidOutputSkipsGatesAndFails(t *testing.T) {
	g := singleNodeGraph("n1")
	g.Nodes[0].Retry = RetryPolicy{MaxAttempts: 1} // exhaust immediately, force routing
	state := startedRun(t, g)
	exec, _ := state.current("n1")
	state, _, _ = Advance(state, NodeExecutionStarted{RunID: testRunID, NodeID: "n1", ExecID: exec.ExecID, Attempt: 1, Iteration: 1}, time.Unix(0, 0))

	state, cmds, err := Advance(state, NodeOutputReceived{RunID: testRunID, NodeID: "n1", ExecID: exec.ExecID, SchemaValid: false}, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if cmds != nil {
		t.Errorf("expected no cmds (edge routes straight to $fail), got %v", cmds)
	}
	if state.Status != RunFailed {
		t.Errorf("run Status = %s, want %s", state.Status, RunFailed)
	}
	got, _ := state.current("n1")
	if got.Status != ExecFailed || got.Reason != FailSchemaInvalid {
		t.Errorf("exec = %+v, want Status=failed Reason=schema-invalid", got)
	}
}

func TestAdvance_validOutputRequestsGateEvaluation(t *testing.T) {
	state := startedRun(t, singleNodeGraph("n1"))
	exec, _ := state.current("n1")
	state, _, _ = Advance(state, NodeExecutionStarted{RunID: testRunID, NodeID: "n1", ExecID: exec.ExecID}, time.Unix(0, 0))

	_, cmds, err := Advance(state, NodeOutputReceived{RunID: testRunID, NodeID: "n1", ExecID: exec.ExecID, SchemaValid: true}, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if len(cmds) != 1 {
		t.Fatalf("cmds = %v, want exactly one CmdEvaluateGates", cmds)
	}
	if _, ok := cmds[0].(CmdEvaluateGates); !ok {
		t.Errorf("cmds[0] = %T, want CmdEvaluateGates", cmds[0])
	}
}

func TestAdvance_gatesPassedRoutesToSuccessSink(t *testing.T) {
	state := startedRun(t, singleNodeGraph("n1"))
	exec, _ := state.current("n1")
	state, _, _ = Advance(state, NodeExecutionStarted{RunID: testRunID, NodeID: "n1", ExecID: exec.ExecID}, time.Unix(0, 0))
	state, _, _ = Advance(state, NodeOutputReceived{RunID: testRunID, NodeID: "n1", ExecID: exec.ExecID, SchemaValid: true}, time.Unix(0, 0))

	state, _, err := Advance(state, NodeGatesEvaluated{RunID: testRunID, NodeID: "n1", ExecID: exec.ExecID, Passed: true}, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if state.Status != RunSucceeded {
		t.Errorf("Status = %s, want %s", state.Status, RunSucceeded)
	}
	got, _ := state.current("n1")
	if got.Status != ExecSucceeded {
		t.Errorf("exec Status = %s, want %s", got.Status, ExecSucceeded)
	}
}

func TestAdvance_gatesRejectedWithNoFindingsIsAnError(t *testing.T) {
	state := startedRun(t, singleNodeGraph("n1"))
	exec, _ := state.current("n1")
	state, _, _ = Advance(state, NodeExecutionStarted{RunID: testRunID, NodeID: "n1", ExecID: exec.ExecID}, time.Unix(0, 0))
	state, _, _ = Advance(state, NodeOutputReceived{RunID: testRunID, NodeID: "n1", ExecID: exec.ExecID, SchemaValid: true}, time.Unix(0, 0))

	_, _, err := Advance(state, NodeGatesEvaluated{RunID: testRunID, NodeID: "n1", ExecID: exec.ExecID, Passed: false}, time.Unix(0, 0))
	if !errors.Is(err, ErrRejectedNeedsFindings) {
		t.Fatalf("err = %v, want ErrRejectedNeedsFindings", err)
	}
}

func TestAdvance_rejectedRoutesToSelfBoundedByLoopGuard(t *testing.T) {
	g := singleNodeGraph("n1")
	g.Nodes[0].LoopGuard = LoopGuard{MaxIterationsPerNode: 2}
	state := startedRun(t, g)
	exec, _ := state.current("n1")
	state, _, _ = Advance(state, NodeExecutionStarted{RunID: testRunID, NodeID: "n1", ExecID: exec.ExecID}, time.Unix(0, 0))
	state, _, _ = Advance(state, NodeOutputReceived{RunID: testRunID, NodeID: "n1", ExecID: exec.ExecID, SchemaValid: true}, time.Unix(0, 0))

	findings := []Finding{{ID: "f1", Message: "todo left behind"}}
	state, cmds, err := Advance(state, NodeGatesEvaluated{RunID: testRunID, NodeID: "n1", ExecID: exec.ExecID, Passed: false, Findings: findings}, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if state.Status != RunRunning {
		t.Errorf("run Status = %s, want %s (still in progress)", state.Status, RunRunning)
	}
	if len(cmds) != 1 {
		t.Fatalf("cmds = %v, want exactly one CmdStartNode (self-loop)", cmds)
	}
	start, ok := cmds[0].(CmdStartNode)
	if !ok || start.NodeID != "n1" || start.Iteration != 2 || start.Attempt != 1 {
		t.Errorf("cmds[0] = %+v, want CmdStartNode{NodeID: n1, Attempt: 1, Iteration: 2}", cmds[0])
	}
	execs := state.Executions["n1"]
	if len(execs) != 2 {
		t.Fatalf("Executions[n1] = %v, want 2 rows (rejected + retried)", execs)
	}
	if execs[0].Status != ExecRejected || len(execs[0].Findings) != 1 {
		t.Errorf("first exec = %+v, want Status=rejected with 1 finding", execs[0])
	}
	if execs[1].PriorExecID != execs[0].ExecID {
		t.Errorf("second exec PriorExecID = %s, want %s", execs[1].PriorExecID, execs[0].ExecID)
	}
}

func TestAdvance_loopGuardExceededParksAndAsksAHuman(t *testing.T) {
	g := singleNodeGraph("n1")
	g.Nodes[0].LoopGuard = LoopGuard{MaxIterationsPerNode: 2}
	state := startedRun(t, g)

	findings := []Finding{{ID: "f1", Message: "still broken"}}
	// Iteration 1 -> rejected, loops (1 < 2).
	exec, _ := state.current("n1")
	state, _, _ = Advance(state, NodeExecutionStarted{RunID: testRunID, NodeID: "n1", ExecID: exec.ExecID}, time.Unix(0, 0))
	state, _, _ = Advance(state, NodeOutputReceived{RunID: testRunID, NodeID: "n1", ExecID: exec.ExecID, SchemaValid: true}, time.Unix(0, 0))
	state, _, err := Advance(state, NodeGatesEvaluated{RunID: testRunID, NodeID: "n1", ExecID: exec.ExecID, Passed: false, Findings: findings}, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("iteration 1: %v", err)
	}

	// Iteration 2 -> rejected again; 2 >= MaxIterationsPerNode(2), so this
	// must park and escalate rather than loop a third time.
	exec, _ = state.current("n1")
	state, _, _ = Advance(state, NodeExecutionStarted{RunID: testRunID, NodeID: "n1", ExecID: exec.ExecID}, time.Unix(0, 0))
	state, _, _ = Advance(state, NodeOutputReceived{RunID: testRunID, NodeID: "n1", ExecID: exec.ExecID, SchemaValid: true}, time.Unix(0, 0))
	state, cmds, err := Advance(state, NodeGatesEvaluated{RunID: testRunID, NodeID: "n1", ExecID: exec.ExecID, Passed: false, Findings: findings}, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("iteration 2: %v", err)
	}
	got, _ := state.current("n1")
	if got.Status != ExecParked || got.ParkReason != ParkLoopGuardExceeded {
		t.Errorf("exec = %+v, want Status=parked ParkReason=loop-guard-exceeded", got)
	}
	if len(cmds) != 1 {
		t.Fatalf("cmds = %v, want exactly one CmdCreateHumanTask", cmds)
	}
	if _, ok := cmds[0].(CmdCreateHumanTask); !ok {
		t.Errorf("cmds[0] = %T, want CmdCreateHumanTask", cmds[0])
	}
}

func TestAdvance_retryBoundaryOffByOneOnAttempt(t *testing.T) {
	g := singleNodeGraph("n1")
	g.Nodes[0].Retry = RetryPolicy{MaxAttempts: 2}
	state := startedRun(t, g)

	// Attempt 1 fails; 1 < MaxAttempts(2), so it must retry (Attempt 2),
	// not route to $fail.
	exec, _ := state.current("n1")
	state, _, err := Advance(state, NodeExecutionStarted{RunID: testRunID, NodeID: "n1", ExecID: exec.ExecID}, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("attempt 1 start: %v", err)
	}
	state, cmds, err := Advance(state, NodeExecutionFailed{RunID: testRunID, NodeID: "n1", ExecID: exec.ExecID, Reason: FailFailure}, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("attempt 1: %v", err)
	}
	if state.Status != RunRunning {
		t.Fatalf("Status = %s, want %s after first failure (still retrying)", state.Status, RunRunning)
	}
	start, ok := cmds[0].(CmdStartNode)
	if !ok || start.Attempt != 2 {
		t.Fatalf("cmds[0] = %+v, want CmdStartNode{Attempt: 2}", cmds[0])
	}

	// Attempt 2 fails; 2 >= MaxAttempts(2), so it must route to $fail now.
	exec, _ = state.current("n1")
	state, _, err = Advance(state, NodeExecutionStarted{RunID: testRunID, NodeID: "n1", ExecID: exec.ExecID}, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("attempt 2 start: %v", err)
	}
	state, cmds, err = Advance(state, NodeExecutionFailed{RunID: testRunID, NodeID: "n1", ExecID: exec.ExecID, Reason: FailFailure}, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("attempt 2: %v", err)
	}
	if state.Status != RunFailed {
		t.Errorf("Status = %s, want %s after attempts exhausted", state.Status, RunFailed)
	}
	if cmds != nil {
		t.Errorf("expected no cmds once routed to $fail, got %v", cmds)
	}
}

func TestAdvance_advanceIsDeterministicForIdenticalInputs(t *testing.T) {
	g := singleNodeGraph("n1")
	state := startedRun(t, g)
	exec, _ := state.current("n1")
	ev := NodeExecutionStarted{RunID: testRunID, NodeID: "n1", ExecID: exec.ExecID, Attempt: 1, Iteration: 1}
	now := time.Unix(12345, 0)

	got1, cmds1, err1 := Advance(state, ev, now)
	got2, cmds2, err2 := Advance(state, ev, now)

	if err1 != err2 {
		t.Fatalf("errors differ across identical calls: %v vs %v", err1, err2)
	}
	if got1.Status != got2.Status {
		t.Errorf("Status differs: %v vs %v", got1.Status, got2.Status)
	}
	e1, _ := got1.current("n1")
	e2, _ := got2.current("n1")
	if !reflect.DeepEqual(e1, e2) {
		t.Errorf("NodeExecution differs: %+v vs %+v", e1, e2)
	}
	if len(cmds1) != len(cmds2) {
		t.Errorf("cmd counts differ: %d vs %d", len(cmds1), len(cmds2))
	}
}
