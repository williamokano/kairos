package domain

import (
	"errors"
	"testing"
	"time"
)

// TestAdvance_runLevelEventsRespectTheLegalTransitionTable exercises every
// run-level event against every RunStatus and asserts Advance's verdict
// matches legalRunEvents exactly: ErrIllegalTransition when the (status,
// event) pair is absent from the table, and something other than
// ErrIllegalTransition when it is present.
func TestAdvance_runLevelEventsRespectTheLegalTransitionTable(t *testing.T) {
	now := time.Unix(0, 0)
	runID := "run_1"

	statuses := []RunStatus{RunPending, RunRunning, RunDegradedS, RunSucceeded, RunFailed, RunCancelledS, RunRejectedS}
	events := []Event{
		TriggerReceived{RunID: runID},
		RunStarted{RunID: runID, Graph: singleNodeGraph("n1")},
		RunRejected{RunID: runID, Reason: "preflight"},
		RunCancelled{RunID: runID, Reason: "user"},
		RunDegraded{RunID: runID, Reason: "child failed"},
		RunDegradedResolved{RunID: runID},
	}

	for _, status := range statuses {
		for _, ev := range events {
			state := RunState{ID: runID, Status: status, Graph: singleNodeGraph("n1"), Executions: map[NodeID][]NodeExecution{}}
			wantLegal := legalRunEvent(status, ev.EventType())

			_, _, err := Advance(state, ev, now)

			gotIllegal := errors.Is(err, ErrIllegalTransition)
			if wantLegal && gotIllegal {
				t.Errorf("status=%s event=%s: legal per table but Advance returned ErrIllegalTransition", status, ev.EventType())
			}
			if !wantLegal && !gotIllegal {
				t.Errorf("status=%s event=%s: illegal per table but Advance did not return ErrIllegalTransition (err=%v)", status, ev.EventType(), err)
			}
		}
	}
}

// TestAdvance_nodeLevelEventsRespectTheLegalTransitionTable does the same
// for node-level events against every ExecStatus, holding RunStatus fixed
// at RunRunning (the only status under which node events are legal at all)
// so only the ExecStatus dimension is under test.
func TestAdvance_nodeLevelEventsRespectTheLegalTransitionTable(t *testing.T) {
	now := time.Unix(0, 0)
	runID := "run_1"
	nodeID := NodeID("n1")
	execID := "exec_1"

	statuses := []ExecStatus{
		ExecPending, ExecExecuting, ExecWaiting, ExecAdopted,
		ExecSucceeded, ExecFailed, ExecRejected, ExecInterrupted, ExecLost, ExecParked,
	}
	events := []Event{
		NodeExecutionStarted{RunID: runID, NodeID: string(nodeID), ExecID: execID, Attempt: 1, Iteration: 1},
		NodeOutputReceived{RunID: runID, NodeID: string(nodeID), ExecID: execID, SchemaValid: true},
		NodeWaitResolved{RunID: runID, NodeID: string(nodeID), ExecID: execID, Outcome: WaitMatched, SchemaValid: true},
		NodeGatesEvaluated{RunID: runID, NodeID: string(nodeID), ExecID: execID, Passed: true},
		NodeExecutionFailed{RunID: runID, NodeID: string(nodeID), ExecID: execID, Reason: FailFailure},
		NodeExecutionInterrupted{RunID: runID, NodeID: string(nodeID), ExecID: execID},
		NodeExecutionLost{RunID: runID, NodeID: string(nodeID), ExecID: execID},
		NodeExecutionAdopted{RunID: runID, NodeID: string(nodeID), ExecID: execID},
		HumanTaskAnswered{RunID: runID, NodeID: string(nodeID), ExecID: execID, SchemaValid: true},
	}

	for _, status := range statuses {
		for _, ev := range events {
			state := RunState{
				ID:     runID,
				Status: RunRunning,
				Graph:  singleNodeGraph(nodeID),
				Executions: map[NodeID][]NodeExecution{
					nodeID: {{ExecID: execID, NodeID: nodeID, Status: status, Attempt: 1, Iteration: 1}},
				},
			}
			wantLegal := legalExecEvent(status, ev.EventType())

			_, _, err := Advance(state, ev, now)

			gotIllegal := errors.Is(err, ErrIllegalTransition)
			if wantLegal && gotIllegal {
				t.Errorf("execStatus=%s event=%s: legal per table but Advance returned ErrIllegalTransition", status, ev.EventType())
			}
			if !wantLegal && !gotIllegal {
				t.Errorf("execStatus=%s event=%s: illegal per table but Advance did not return ErrIllegalTransition (err=%v)", status, ev.EventType(), err)
			}
		}
	}
}

// singleNodeGraph builds a minimal resolved Graph with one node whose every
// outcome trigger routes to a sink, a moderate retry/loop-guard budget, and
// no wait — reused across transitions_test.go and advance_test.go.
func singleNodeGraph(id NodeID) Graph {
	return Graph{
		Entry: id,
		Nodes: []Node{{
			ID:        id,
			Retry:     RetryPolicy{MaxAttempts: 2},
			LoopGuard: LoopGuard{MaxIterationsPerNode: 3},
		}},
		Edges: map[NodeID]map[EdgeTrigger]NodeID{
			id: {
				OnSuccess: SinkSucceed,
				OnFailure: SinkFail,
				OnTimeout: SinkFail,
			},
		},
	}
}
