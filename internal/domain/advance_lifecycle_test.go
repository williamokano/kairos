package domain

import (
	"testing"
	"time"
)

// waitGraph builds a two-node graph: "gate" runs normally, "approve" waits
// on the given WaitSpec before routing to $succeed/$fail.
func waitGraph(wait WaitSpec) Graph {
	return Graph{
		Entry: "approve",
		Nodes: []Node{
			{ID: "approve", Wait: &wait, Retry: RetryPolicy{MaxAttempts: 1}, LoopGuard: LoopGuard{MaxIterationsPerNode: 1}},
		},
		Edges: map[NodeID]map[EdgeTrigger]NodeID{
			"approve": {
				OnSuccess: SinkSucceed,
				OnFailure: SinkFail,
				OnTimeout: SinkFail,
			},
		},
	}
}

func TestAdvance_humanApprovalWaitsThenResumesOnAnswer(t *testing.T) {
	wait := WaitSpec{Kind: WaitHuman, OnTimeout: OnTimeoutEscalate}
	state := startedRun(t, waitGraph(wait))

	exec, ok := state.current("approve")
	if !ok || exec.Status != ExecWaiting {
		t.Fatalf("exec = %+v (ok=%v), want Status=waiting immediately after dispatch", exec, ok)
	}

	state, cmds, err := Advance(state, HumanTaskCreated{RunID: testRunID, NodeID: "approve", ExecID: exec.ExecID}, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("HumanTaskCreated: %v", err)
	}
	if cmds != nil {
		t.Errorf("expected no cmds from HumanTaskCreated, got %v", cmds)
	}

	state, cmds, err = Advance(state, HumanTaskAnswered{RunID: testRunID, NodeID: "approve", ExecID: exec.ExecID, SchemaValid: true}, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("HumanTaskAnswered: %v", err)
	}
	if len(cmds) != 1 {
		t.Fatalf("cmds = %v, want exactly one CmdEvaluateGates", cmds)
	}
	got, _ := state.current("approve")
	if got.Status != ExecExecuting {
		t.Errorf("Status = %s, want %s", got.Status, ExecExecuting)
	}

	state, _, err = Advance(state, NodeGatesEvaluated{RunID: testRunID, NodeID: "approve", ExecID: exec.ExecID, Passed: true}, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("NodeGatesEvaluated: %v", err)
	}
	if state.Status != RunSucceeded {
		t.Errorf("run Status = %s, want %s", state.Status, RunSucceeded)
	}
}

func TestAdvance_ciWatchPollTimeoutWithParkLeavesNodeWaiting(t *testing.T) {
	timeoutAt := time.Unix(1000, 0)
	wait := WaitSpec{Kind: WaitPoll, TimeoutAt: &timeoutAt, OnTimeout: OnTimeoutPark}
	state := startedRun(t, waitGraph(wait))
	exec, _ := state.current("approve")

	state, cmds, err := Advance(state, NodeWaitResolved{RunID: testRunID, NodeID: "approve", ExecID: exec.ExecID, Outcome: WaitTimedOut}, time.Unix(1000, 0))
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if cmds != nil {
		t.Errorf("onTimeout: park must produce no cmds, got %v", cmds)
	}
	got, _ := state.current("approve")
	if got.Status != ExecWaiting {
		t.Errorf("Status = %s, want %s (park never proceeds and never fails)", got.Status, ExecWaiting)
	}
	if !got.Overdue {
		t.Error("expected Overdue=true after a park timeout")
	}
	if state.Status != RunRunning {
		t.Errorf("run Status = %s, want %s", state.Status, RunRunning)
	}
}

func TestAdvance_ciWatchPollTimeoutWithEscalateParksTheExecution(t *testing.T) {
	timeoutAt := time.Unix(1000, 0)
	wait := WaitSpec{Kind: WaitPoll, TimeoutAt: &timeoutAt, OnTimeout: OnTimeoutEscalate}
	state := startedRun(t, waitGraph(wait))
	exec, _ := state.current("approve")

	state, cmds, err := Advance(state, NodeWaitResolved{RunID: testRunID, NodeID: "approve", ExecID: exec.ExecID, Outcome: WaitTimedOut}, time.Unix(1000, 0))
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	got, _ := state.current("approve")
	if got.Status != ExecParked || got.ParkReason != ParkWaitTimeoutEscalate {
		t.Errorf("exec = %+v, want Status=parked ParkReason=wait-timeout-escalate", got)
	}
	if len(cmds) != 1 {
		t.Fatalf("cmds = %v, want exactly one CmdCreateHumanTask", cmds)
	}
	if _, ok := cmds[0].(CmdCreateHumanTask); !ok {
		t.Errorf("cmds[0] = %T, want CmdCreateHumanTask", cmds[0])
	}
}

func TestAdvance_waitSpecWithTimeoutAlwaysArmsATimer(t *testing.T) {
	timeoutAt := time.Unix(1000, 0)
	wait := WaitSpec{Kind: WaitPoll, TimeoutAt: &timeoutAt, OnTimeout: OnTimeoutEscalate}
	_, cmds, err := Advance(RunState{ID: testRunID, Status: RunPending, Executions: map[NodeID][]NodeExecution{}}, RunStarted{RunID: testRunID, Graph: waitGraph(wait)}, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	var sawEnterWait, sawArmTimer bool
	for _, c := range cmds {
		switch c.(type) {
		case CmdEnterWait:
			sawEnterWait = true
		case CmdArmTimer:
			sawArmTimer = true
		}
	}
	if !sawEnterWait || !sawArmTimer {
		t.Fatalf("cmds = %v, want both CmdEnterWait and CmdArmTimer when WaitSpec.TimeoutAt is set", cmds)
	}
}

// TestAdvance_killMidNodeThenRestartRecordsLostThenRetries mirrors the L05
// milestone's exact expectation (12-build-plan.md): a node the
// reconciliation scan cannot verify survived a restart is not different,
// for retry purposes, from one that failed outright — NodeExecutionLost
// finalises the current attempt as terminal Lost and, exactly like
// NodeExecutionFailed, either allocates the next attempt (bounded by
// RetryPolicy.MaxAttempts) or routes via the failure edge once attempts
// are exhausted. This is what lets the engine (L05) re-dispatch a fresh
// NodeExecutionStarted purely by feeding NodeExecutionLost back through
// Advance, with no engine-side retry logic of its own.
func TestAdvance_killMidNodeThenRestartRecordsLostThenRetries(t *testing.T) {
	g := singleNodeGraph("build") // MaxAttempts: 2
	state := startedRun(t, g)
	exec, _ := state.current("build")

	state, _, err := Advance(state, NodeExecutionStarted{RunID: testRunID, NodeID: "build", ExecID: exec.ExecID, Attempt: 1, Iteration: 1}, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("NodeExecutionStarted: %v", err)
	}

	// The daemon is SIGKILLed here — nothing more is recorded for this
	// exec until restart's reconciliation scan runs.
	state, cmds, err := Advance(state, NodeExecutionLost{RunID: testRunID, NodeID: "build", ExecID: exec.ExecID}, time.Unix(100, 0))
	if err != nil {
		t.Fatalf("NodeExecutionLost: %v", err)
	}

	execs := state.Executions["build"]
	if len(execs) != 2 {
		t.Fatalf("Executions[build] = %v, want 2 rows (lost attempt 1 + the retried attempt 2)", execs)
	}
	if execs[0].Status != ExecLost || !execs[0].Status.Terminal() {
		t.Errorf("attempt 1 = %+v, want terminal Status=lost", execs[0])
	}
	if execs[1].Status != ExecPending || execs[1].Attempt != 2 || execs[1].PriorExecID != execs[0].ExecID {
		t.Errorf("attempt 2 = %+v, want Status=pending Attempt=2 PriorExecID=%s", execs[1], execs[0].ExecID)
	}

	start, ok := cmds[0].(CmdStartNode)
	if !ok || start.Attempt != 2 {
		t.Fatalf("cmds[0] = %+v, want CmdStartNode{Attempt: 2}", cmds[0])
	}

	// The engine confirms the retry exactly like any other dispatch — no
	// special-casing needed for a retry born from Lost vs from Failed.
	state, _, err = Advance(state, NodeExecutionStarted{RunID: testRunID, NodeID: "build", ExecID: start.ExecID, Attempt: 2, Iteration: 1}, time.Unix(101, 0))
	if err != nil {
		t.Fatalf("confirming the retried attempt: %v", err)
	}
	got, _ := state.current("build")
	if got.Status != ExecExecuting {
		t.Errorf("Status = %s, want %s", got.Status, ExecExecuting)
	}
}

// TestAdvance_lostRoutesToFailOnceAttemptsAreExhausted is the boundary
// case: MaxAttempts: 1 means the very first Lost verdict already
// exhausts retries, so recovery must route to $fail rather than loop.
func TestAdvance_lostRoutesToFailOnceAttemptsAreExhausted(t *testing.T) {
	g := singleNodeGraph("build")
	g.Nodes[0].Retry = RetryPolicy{MaxAttempts: 1}
	state := startedRun(t, g)
	exec, _ := state.current("build")

	state, _, err := Advance(state, NodeExecutionStarted{RunID: testRunID, NodeID: "build", ExecID: exec.ExecID}, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("NodeExecutionStarted: %v", err)
	}

	state, cmds, err := Advance(state, NodeExecutionLost{RunID: testRunID, NodeID: "build", ExecID: exec.ExecID}, time.Unix(100, 0))
	if err != nil {
		t.Fatalf("NodeExecutionLost: %v", err)
	}
	if cmds != nil {
		t.Errorf("expected no cmds once routed to $fail, got %v", cmds)
	}
	if state.Status != RunFailed {
		t.Errorf("run Status = %s, want %s", state.Status, RunFailed)
	}
}
