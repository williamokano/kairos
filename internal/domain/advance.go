package domain

import (
	"fmt"
	"time"
)

// Advance is the entire domain: state[N] = Advance(state[N-1], events[N],
// now). It is pure — no I/O, no clock reads, no randomness, no generated
// IDs (AGENTS.md §4 rule 4). now and every ID referenced by ev are the
// caller's responsibility; NodeExecution IDs are instead deterministically
// derived from (NodeID, Attempt, Iteration) by execID, so domain can name
// the row it is about to ask the engine to create without minting anything.
//
// It returns the new RunState (the fold of ev into state) and the Cmds
// newly produced by this specific transition — never an accumulation of
// past Cmds. The caller (from L05 on) appends RunAdvanced{causationSeq:
// ev.Seq, cmds} before dispatching any of them ("decision BEFORE action",
// 01-architecture.md).
func Advance(state RunState, ev Event, now time.Time) (RunState, []Cmd, error) {
	switch e := ev.(type) {
	case TriggerReceived:
		return advanceTriggerReceived(state, e)
	case RunStarted:
		return advanceRunStarted(state, e)
	case RunRejected:
		return advanceRunRejected(state, e)
	case RunCancelled:
		return advanceRunCancelled(state, e)
	case RunDegraded:
		return advanceRunDegraded(state, e)
	case RunDegradedResolved:
		return advanceRunDegradedResolved(state, e)
	case NodeExecutionStarted:
		return advanceNodeExecutionStarted(state, e)
	case NodeOutputReceived:
		return advanceNodeOutputReceived(state, e, now)
	case NodeWaitResolved:
		return advanceNodeWaitResolved(state, e, now)
	case NodeGatesEvaluated:
		return advanceNodeGatesEvaluated(state, e, now)
	case NodeExecutionFailed:
		return advanceNodeExecutionFailed(state, e, now)
	case NodeExecutionInterrupted:
		return advanceNodeExecutionInterrupted(state, e)
	case NodeExecutionLost:
		return advanceNodeExecutionLost(state, e, now)
	case NodeExecutionAdopted:
		return advanceNodeExecutionAdopted(state, e)
	case HumanTaskCreated:
		return advanceHumanTaskCreated(state, e)
	case HumanTaskAnswered:
		return advanceHumanTaskAnswered(state, e, now)
	case LLMSessionStarted, SessionResumeFailed, SessionCostUnavailable, OutputRepairAttempted:
		// L08's audit-only facts: they record something true about a
		// NodeExecution's actor invocation without moving it through any
		// state the run's routing cares about (ExecStatus is untouched).
		// Explicit no-op cases rather than falling through to default,
		// since these DO belong to a run's own stream (unlike the L05
		// system-stream events) and must not error as unknown.
		return state, nil, nil
	default:
		return state, nil, fmt.Errorf("%w: %T", ErrUnknownEvent, ev)
	}
}

// execID deterministically names a NodeExecution row from its coordinates,
// so domain can reference a row it hasn't seen created yet without minting
// an ID (only the local executor's IDs are random; this one is pure).
func execID(nodeID NodeID, attempt, iteration int) string {
	return fmt.Sprintf("%s#a%d.i%d", nodeID, attempt, iteration)
}

// --- run-level handlers -----------------------------------------------

func advanceTriggerReceived(state RunState, e TriggerReceived) (RunState, []Cmd, error) {
	if state.Status != "" && !legalRunEvent(state.Status, e.EventType()) {
		return state, nil, ErrIllegalTransition
	}
	return RunState{
		ID:         e.RunID,
		Status:     RunPending,
		Executions: map[NodeID][]NodeExecution{},
	}, nil, nil
}

func advanceRunStarted(state RunState, e RunStarted) (RunState, []Cmd, error) {
	if !legalRunEvent(state.Status, e.EventType()) {
		return state, nil, ErrIllegalTransition
	}
	state = state.withStatus(RunRunning)
	state.Graph = e.Graph
	entry, ok := state.Graph.NodeByID(state.Graph.Entry)
	if !ok {
		return state, nil, fmt.Errorf("%w: entry node %q", ErrUnknownNode, state.Graph.Entry)
	}
	return dispatchExec(state, entry, 1, 1, "")
}

func advanceRunRejected(state RunState, e RunRejected) (RunState, []Cmd, error) {
	if !legalRunEvent(state.Status, e.EventType()) {
		return state, nil, ErrIllegalTransition
	}
	return state.withStatus(RunRejectedS), nil, nil
}

func advanceRunCancelled(state RunState, e RunCancelled) (RunState, []Cmd, error) {
	if !legalRunEvent(state.Status, e.EventType()) {
		return state, nil, ErrIllegalTransition
	}
	var cmds []Cmd
	for nodeID, execs := range state.Executions {
		if len(execs) == 0 {
			continue
		}
		last := execs[len(execs)-1]
		if !last.Status.Terminal() {
			cmds = append(cmds, CmdSignalNode{RunID: state.ID, NodeID: string(nodeID), ExecID: last.ExecID})
		}
	}
	return state.withStatus(RunCancelledS), cmds, nil
}

func advanceRunDegraded(state RunState, e RunDegraded) (RunState, []Cmd, error) {
	if !legalRunEvent(state.Status, e.EventType()) {
		return state, nil, ErrIllegalTransition
	}
	return state.withStatus(RunDegradedS), nil, nil
}

func advanceRunDegradedResolved(state RunState, e RunDegradedResolved) (RunState, []Cmd, error) {
	if !legalRunEvent(state.Status, e.EventType()) {
		return state, nil, ErrIllegalTransition
	}
	return state.withStatus(RunRunning), nil, nil
}

// --- node-level handlers -------------------------------------------------

func advanceNodeExecutionStarted(state RunState, e NodeExecutionStarted) (RunState, []Cmd, error) {
	if !legalRunEvent(state.Status, e.EventType()) {
		return state, nil, ErrIllegalTransition
	}
	exec, err := currentExec(state, NodeID(e.NodeID), e.ExecID)
	if err != nil {
		return state, nil, err
	}
	if !legalExecEvent(exec.Status, e.EventType()) {
		return state, nil, ErrIllegalTransition
	}
	exec.Status = ExecExecuting
	return state.withExecution(exec), nil, nil
}

func advanceNodeOutputReceived(state RunState, e NodeOutputReceived, now time.Time) (RunState, []Cmd, error) {
	if !legalRunEvent(state.Status, e.EventType()) {
		return state, nil, ErrIllegalTransition
	}
	exec, err := currentExec(state, NodeID(e.NodeID), e.ExecID)
	if err != nil {
		return state, nil, err
	}
	if !legalExecEvent(exec.Status, e.EventType()) {
		return state, nil, ErrIllegalTransition
	}
	if !e.SchemaValid {
		return handleFailureOutcome(state, exec, FailSchemaInvalid, "schema validation failed", now)
	}
	exec.Status = ExecExecuting // output received, gate evaluation pending
	state = state.withExecution(exec)
	return state, []Cmd{CmdEvaluateGates{RunID: state.ID, NodeID: string(exec.NodeID), ExecID: exec.ExecID}}, nil
}

func advanceNodeWaitResolved(state RunState, e NodeWaitResolved, now time.Time) (RunState, []Cmd, error) {
	if !legalRunEvent(state.Status, e.EventType()) {
		return state, nil, ErrIllegalTransition
	}
	exec, err := currentExec(state, NodeID(e.NodeID), e.ExecID)
	if err != nil {
		return state, nil, err
	}
	if !legalExecEvent(exec.Status, e.EventType()) {
		return state, nil, ErrIllegalTransition
	}
	node, ok := state.Graph.NodeByID(exec.NodeID)
	if !ok {
		return state, nil, ErrUnknownNode
	}
	if e.Outcome == WaitTimedOut {
		return handleWaitTimeout(state, exec, node)
	}
	if !e.SchemaValid {
		return handleFailureOutcome(state, exec, FailSchemaInvalid, "schema validation failed", now)
	}
	exec.Status = ExecExecuting
	state = state.withExecution(exec)
	return state, []Cmd{CmdEvaluateGates{RunID: state.ID, NodeID: string(exec.NodeID), ExecID: exec.ExecID}}, nil
}

func advanceNodeGatesEvaluated(state RunState, e NodeGatesEvaluated, now time.Time) (RunState, []Cmd, error) {
	if !legalRunEvent(state.Status, e.EventType()) {
		return state, nil, ErrIllegalTransition
	}
	exec, err := currentExec(state, NodeID(e.NodeID), e.ExecID)
	if err != nil {
		return state, nil, err
	}
	if !legalExecEvent(exec.Status, e.EventType()) {
		return state, nil, ErrIllegalTransition
	}
	if !e.Passed && len(e.Findings) == 0 {
		return state, nil, ErrRejectedNeedsFindings
	}
	node, ok := state.Graph.NodeByID(exec.NodeID)
	if !ok {
		return state, nil, ErrUnknownNode
	}
	if e.Passed {
		exec.Status = ExecSucceeded
		state = state.withExecution(exec)
		return routeViaEdge(state, exec.NodeID, OnSuccess, now)
	}
	// Rejected: loop back to the same node (bounded by LoopGuard) or park
	// and escalate to a human once the bound is exceeded.
	if exec.Iteration >= node.LoopGuard.MaxIterationsPerNode {
		exec.Status = ExecParked
		exec.ParkReason = ParkLoopGuardExceeded
		exec.Findings = e.Findings
		state = state.withExecution(exec)
		return state, []Cmd{CmdCreateHumanTask{RunID: state.ID, NodeID: string(exec.NodeID), ExecID: exec.ExecID}}, nil
	}
	exec.Status = ExecRejected
	exec.Findings = e.Findings
	state = state.withExecution(exec)
	return dispatchExec(state, node, 1, exec.Iteration+1, exec.ExecID)
}

func advanceNodeExecutionFailed(state RunState, e NodeExecutionFailed, now time.Time) (RunState, []Cmd, error) {
	if !legalRunEvent(state.Status, e.EventType()) {
		return state, nil, ErrIllegalTransition
	}
	exec, err := currentExec(state, NodeID(e.NodeID), e.ExecID)
	if err != nil {
		return state, nil, err
	}
	if !legalExecEvent(exec.Status, e.EventType()) {
		return state, nil, ErrIllegalTransition
	}
	return handleFailureOutcome(state, exec, e.Reason, e.Message, now)
}

func advanceNodeExecutionInterrupted(state RunState, e NodeExecutionInterrupted) (RunState, []Cmd, error) {
	if !legalRunEvent(state.Status, e.EventType()) {
		return state, nil, ErrIllegalTransition
	}
	exec, err := currentExec(state, NodeID(e.NodeID), e.ExecID)
	if err != nil {
		return state, nil, err
	}
	if !legalExecEvent(exec.Status, e.EventType()) {
		return state, nil, ErrIllegalTransition
	}
	exec.Status = ExecInterrupted
	return state.withExecution(exec), nil, nil
}

// advanceNodeExecutionLost finalises exec as Lost and, like
// handleFailureOutcome, either allocates the next retry attempt (bounded
// by RetryPolicy.MaxAttempts) or routes via the node's failure edge once
// attempts are exhausted. A node the reconciliation scan (L05) cannot
// verify survived a restart is not different, for retry purposes, from
// one that failed outright — both mean "this attempt produced no
// confirmed outcome" — so Lost reuses the same bounded-retry shape
// Failed already has, rather than requiring the engine to reimplement
// retry/route logic outside domain (12-build-plan.md: "L05 (engine)...
// decides when to dispatch a fresh NodeExecutionStarted for a Lost...
// node", which this makes possible by feeding NodeExecutionLost back
// through Advance like any other outcome event).
func advanceNodeExecutionLost(state RunState, e NodeExecutionLost, now time.Time) (RunState, []Cmd, error) {
	if !legalRunEvent(state.Status, e.EventType()) {
		return state, nil, ErrIllegalTransition
	}
	exec, err := currentExec(state, NodeID(e.NodeID), e.ExecID)
	if err != nil {
		return state, nil, err
	}
	if !legalExecEvent(exec.Status, e.EventType()) {
		return state, nil, ErrIllegalTransition
	}

	exec.Status = ExecLost
	state = state.withExecution(exec)

	node, ok := state.Graph.NodeByID(exec.NodeID)
	if !ok {
		return state, nil, ErrUnknownNode
	}
	if exec.Attempt < node.Retry.MaxAttempts {
		return dispatchExec(state, node, exec.Attempt+1, exec.Iteration, exec.ExecID)
	}
	return routeViaEdge(state, exec.NodeID, OnFailure, now)
}

func advanceNodeExecutionAdopted(state RunState, e NodeExecutionAdopted) (RunState, []Cmd, error) {
	if !legalRunEvent(state.Status, e.EventType()) {
		return state, nil, ErrIllegalTransition
	}
	exec, err := currentExec(state, NodeID(e.NodeID), e.ExecID)
	if err != nil {
		return state, nil, err
	}
	if !legalExecEvent(exec.Status, e.EventType()) {
		return state, nil, ErrIllegalTransition
	}
	exec.Status = ExecAdopted
	return state.withExecution(exec), nil, nil
}

func advanceHumanTaskCreated(state RunState, e HumanTaskCreated) (RunState, []Cmd, error) {
	if !legalRunEvent(state.Status, e.EventType()) {
		return state, nil, ErrIllegalTransition
	}
	// Informational: HumanTaskCreated does not itself move a NodeExecution;
	// it records that the human queue now shows the task CmdCreateHumanTask
	// asked for. Validate the execution exists so a task can't be created
	// for a node with no current execution.
	if _, err := currentExec(state, NodeID(e.NodeID), e.ExecID); err != nil {
		return state, nil, err
	}
	return state, nil, nil
}

func advanceHumanTaskAnswered(state RunState, e HumanTaskAnswered, now time.Time) (RunState, []Cmd, error) {
	if !legalRunEvent(state.Status, e.EventType()) {
		return state, nil, ErrIllegalTransition
	}
	exec, err := currentExec(state, NodeID(e.NodeID), e.ExecID)
	if err != nil {
		return state, nil, err
	}
	if !legalExecEvent(exec.Status, e.EventType()) {
		return state, nil, ErrIllegalTransition
	}
	if !e.SchemaValid {
		return handleFailureOutcome(state, exec, FailSchemaInvalid, "schema validation failed", now)
	}
	exec.Status = ExecExecuting
	state = state.withExecution(exec)
	return state, []Cmd{CmdEvaluateGates{RunID: state.ID, NodeID: string(exec.NodeID), ExecID: exec.ExecID}}, nil
}

// --- shared routing/dispatch logic ---------------------------------------

// currentExec returns the current NodeExecution for nodeID, verifying it
// matches execID.
func currentExec(state RunState, nodeID NodeID, execID string) (NodeExecution, error) {
	exec, ok := state.current(nodeID)
	if !ok {
		return NodeExecution{}, fmt.Errorf("%w: node %q", ErrNoCurrentExecution, nodeID)
	}
	if exec.ExecID != execID {
		return NodeExecution{}, fmt.Errorf("%w: node %q", ErrExecIDMismatch, nodeID)
	}
	return exec, nil
}

// handleWaitTimeout applies 03-workflows.md's onTimeout split: `park` never
// transitions the execution (only sets Overdue); `escalate` moves it to
// Parked and asks for a human.
func handleWaitTimeout(state RunState, exec NodeExecution, node Node) (RunState, []Cmd, error) {
	if node.Wait == nil {
		return state, nil, ErrUnknownNode
	}
	if node.Wait.OnTimeout == OnTimeoutPark {
		exec.Overdue = true
		return state.withExecution(exec), nil, nil
	}
	exec.Status = ExecParked
	exec.ParkReason = ParkWaitTimeoutEscalate
	state = state.withExecution(exec)
	return state, []Cmd{CmdCreateHumanTask{RunID: state.ID, NodeID: string(exec.NodeID), ExecID: exec.ExecID}}, nil
}

// handleFailureOutcome finalises exec as Failed and either allocates the
// next retry attempt (bounded by RetryPolicy.MaxAttempts) or routes via the
// node's failure/timeout Graph edge once attempts are exhausted.
// FailCancelled never retries.
func handleFailureOutcome(state RunState, exec NodeExecution, reason FailReason, _ string, now time.Time) (RunState, []Cmd, error) {
	exec.Status = ExecFailed
	exec.Reason = reason
	state = state.withExecution(exec)

	node, ok := state.Graph.NodeByID(exec.NodeID)
	if !ok {
		return state, nil, ErrUnknownNode
	}

	if reason != FailCancelled && exec.Attempt < node.Retry.MaxAttempts {
		return dispatchExec(state, node, exec.Attempt+1, exec.Iteration, exec.ExecID)
	}

	trigger := OnFailure
	if reason == FailTimeout {
		trigger = OnTimeout
	}
	return routeViaEdge(state, exec.NodeID, trigger, now)
}

// routeViaEdge resolves the Graph edge for (nodeID, trigger) and either
// finalises the Run at a sink or dispatches the next node's first attempt.
func routeViaEdge(state RunState, nodeID NodeID, trigger EdgeTrigger, now time.Time) (RunState, []Cmd, error) {
	_ = now
	next, ok := state.Graph.edge(nodeID, trigger)
	if !ok {
		return state, nil, ErrUnresolvedEdge
	}
	switch next {
	case SinkSucceed:
		return state.withStatus(RunSucceeded), nil, nil
	case SinkFail:
		return state.withStatus(RunFailed), nil, nil
	}
	node, ok := state.Graph.NodeByID(next)
	if !ok {
		return state, nil, ErrUnknownNode
	}
	return dispatchExec(state, node, 1, 1, "")
}

// dispatchExec allocates a NodeExecution for node at (attempt, iteration)
// and returns the Cmds asking the engine to actually run it. A node with a
// static Wait spec is entered directly (Waiting never needs a spawn
// confirmation — 06-durability.md: "a wait's entire footprint is three
// rows"); every other node is dispatched Pending, awaiting the engine's
// NodeExecutionStarted confirmation before it becomes Executing.
func dispatchExec(state RunState, node Node, attempt, iteration int, priorExecID string) (RunState, []Cmd, error) {
	id := execID(node.ID, attempt, iteration)
	exec := NodeExecution{
		ExecID:      id,
		PriorExecID: priorExecID,
		NodeID:      node.ID,
		Attempt:     attempt,
		Iteration:   iteration,
	}

	if node.Wait != nil {
		exec.Status = ExecWaiting
		state = state.withExecution(exec)
		cmds := []Cmd{CmdEnterWait{RunID: state.ID, NodeID: string(node.ID), ExecID: id, Wait: *node.Wait}}
		if node.Wait.Kind == WaitHuman {
			cmds = append(cmds, CmdCreateHumanTask{RunID: state.ID, NodeID: string(node.ID), ExecID: id})
		}
		if node.Wait.TimeoutAt != nil {
			cmds = append(cmds, CmdArmTimer{RunID: state.ID, NodeID: string(node.ID), ExecID: id, FireAt: *node.Wait.TimeoutAt})
		}
		return state, cmds, nil
	}

	exec.Status = ExecPending
	state = state.withExecution(exec)
	return state, []Cmd{CmdStartNode{RunID: state.ID, NodeID: string(node.ID), ExecID: id, Attempt: attempt, Iteration: iteration}}, nil
}
