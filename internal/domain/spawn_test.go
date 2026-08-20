package domain

import (
	"testing"
	"time"
)

// TestAdvance_waitChildRunDispatchesSpawnChildrenAlongsideEnterWait is
// L17's domain-level proof that a spawn/join node is wired through the
// same Wait mechanism L01 already reserved WaitChildRun for — mirroring
// exactly how WaitHuman pairs CmdEnterWait with CmdCreateHumanTask.
func TestAdvance_waitChildRunDispatchesSpawnChildrenAlongsideEnterWait(t *testing.T) {
	wait := WaitSpec{Kind: WaitChildRun, OnTimeout: OnTimeoutPark}

	state, _, err := Advance(RunState{}, TriggerReceived{RunID: testRunID}, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("TriggerReceived: %v", err)
	}
	state, cmds, err := Advance(state, RunStarted{RunID: testRunID, Graph: waitGraph(wait)}, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("RunStarted: %v", err)
	}

	var sawEnterWait, sawSpawnChildren bool
	for _, c := range cmds {
		switch c.(type) {
		case CmdEnterWait:
			sawEnterWait = true
		case CmdSpawnChildren:
			sawSpawnChildren = true
		}
	}
	if !sawEnterWait || !sawSpawnChildren {
		t.Fatalf("cmds = %#v, want both CmdEnterWait and CmdSpawnChildren", cmds)
	}

	exec, ok := state.current("approve")
	if !ok || exec.Status != ExecWaiting {
		t.Fatalf("exec = %+v (ok=%v), want Status=waiting immediately after dispatch", exec, ok)
	}
}

// TestAdvance_waitFailedRoutesViaFailureEdge is the domain-level proof
// that a spawn/join's onChildFailure: fail resolution (NodeWaitResolved{
// Outcome: WaitFailed}) is a real failure — not squeezed into the
// schema-invalid path — and routes through the ordinary failure edge like
// any other ExecFailed outcome.
func TestAdvance_waitFailedRoutesViaFailureEdge(t *testing.T) {
	wait := WaitSpec{Kind: WaitChildRun, OnTimeout: OnTimeoutPark}
	state := startedRun(t, waitGraph(wait))

	exec, ok := state.current("approve")
	if !ok || exec.Status != ExecWaiting {
		t.Fatalf("exec = %+v (ok=%v), want Status=waiting", exec, ok)
	}

	state, _, err := Advance(state, NodeWaitResolved{RunID: testRunID, NodeID: "approve", ExecID: exec.ExecID, Outcome: WaitFailed}, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("NodeWaitResolved{WaitFailed}: %v", err)
	}
	if state.Status != RunFailed {
		t.Errorf("run Status = %s, want %s", state.Status, RunFailed)
	}
	got, _ := state.current("approve")
	if got.Status != ExecFailed || got.Reason != FailFailure {
		t.Errorf("exec = %+v, want Status=failed Reason=failure", got)
	}
}
