package domain

import "encoding/json"

// Event is a fact domain.Advance folds into RunState. Every concrete event
// is a closed member of this sum type (the isEvent marker method). Some
// events look like echoes of Advance's own prior decisions (e.g.
// NodeExecutionStarted, appended right after a CmdStartNode is dispatched)
// — domain does not distinguish "external fact" from "echo of my own
// decision"; both are ordinary Events fed back into Advance.
type Event interface {
	// EventType is the stable, append-only string identifying this event's
	// shape (AGENTS.md §4 rule 6: an event type, once merged, never removed
	// or repurposed). L02 uses this as the event store's event_type column.
	EventType() string
	isEvent()
}

// TriggerReceived is the first event of every run's stream (L15,
// 01-architecture.md: "every run traces to a trigger... named in the Run's
// first event"). ParentRunID is set for a run spawned by another run (L17).
type TriggerReceived struct {
	RunID         string
	TriggerRef    string // opaque; L16 gives this structure
	DefinitionRef string
	Params        json.RawMessage
	ParentRunID   *string
	CorrelationID string
}

func (TriggerReceived) EventType() string { return "trigger.received" }
func (TriggerReceived) isEvent()          {}

// RunStarted carries the fully-resolved Graph. Every default a workflow
// author may omit (document-order edges, rejected->self,
// failure/timeout/denied->$fail) must already be applied — that resolution
// is L03's job, not domain's.
type RunStarted struct {
	RunID string
	Graph Graph
}

func (RunStarted) EventType() string { return "run.started" }
func (RunStarted) isEvent()          {}

// RunRejected records a preflight failure: the run never entered Running
// (06-durability.md).
type RunRejected struct {
	RunID  string
	Reason string
}

func (RunRejected) EventType() string { return "run.rejected" }
func (RunRejected) isEvent()          {}

// RunCancelled records a user- or system-initiated cancellation.
type RunCancelled struct {
	RunID  string
	Reason string
}

func (RunCancelled) EventType() string { return "run.cancelled" }
func (RunCancelled) isEvent()          {}

// RunDegraded is the 03-workflows.md placeholder: Degraded is "a first-class
// state, resolvable only by a coordinator node." The condition that
// produces this fact (a child run's join saw onChildFailure: degrade) is
// computed by L17; domain only folds the fact.
type RunDegraded struct {
	RunID  string
	Reason string
}

func (RunDegraded) EventType() string { return "run.degraded" }
func (RunDegraded) isEvent()          {}

// RunDegradedResolved records a coordinator resolving a Degraded run.
type RunDegradedResolved struct {
	RunID string
}

func (RunDegradedResolved) EventType() string { return "run.degraded.resolved" }
func (RunDegradedResolved) isEvent()          {}

// NodeExecutionStarted records that a NodeExecution has been dispatched for
// actual actor invocation (Pending -> Executing).
type NodeExecutionStarted struct {
	RunID, NodeID, ExecID string
	Attempt, Iteration    int
}

func (NodeExecutionStarted) EventType() string { return "node.execution.started" }
func (NodeExecutionStarted) isEvent()          {}

// NodeOutputReceived is the actor-invocation outcome. SchemaValid=false
// always finalises to Failed{Reason: SchemaInvalid} — 05-gates.md: gates
// never run on invalid output.
type NodeOutputReceived struct {
	RunID, NodeID, ExecID string
	SchemaValid           bool
	Output                json.RawMessage
}

func (NodeOutputReceived) EventType() string { return "node.output.received" }
func (NodeOutputReceived) isEvent()          {}

// WaitOutcome narrows how a wait resolved.
type WaitOutcome string

const (
	WaitMatched  WaitOutcome = "matched"
	WaitTimedOut WaitOutcome = "timed-out"
)

// NodeWaitResolved is the Waiting-path outcome. Outcome=Matched carries an
// Output that flows through the same schema-valid check as
// NodeOutputReceived. Outcome=TimedOut with the node's WaitSpec.OnTimeout ==
// park sets Overdue and performs NO transition; OnTimeout == escalate moves
// to Parked.
type NodeWaitResolved struct {
	RunID, NodeID, ExecID string
	Outcome               WaitOutcome
	SchemaValid           bool
	Output                json.RawMessage
}

func (NodeWaitResolved) EventType() string { return "node.wait.resolved" }
func (NodeWaitResolved) isEvent()          {}

// NodeGatesEvaluated is the aggregate gate-schedule verdict. L10 computes
// per-gate detail; domain only needs the whole-schedule result to route
// success vs rejected. Passed=false REQUIRES len(Findings) > 0.
type NodeGatesEvaluated struct {
	RunID, NodeID, ExecID string
	Passed                bool
	Findings              []Finding
}

func (NodeGatesEvaluated) EventType() string { return "node.gates.evaluated" }
func (NodeGatesEvaluated) isEvent()          {}

// NodeExecutionFailed finalises a NodeExecution as Failed.
type NodeExecutionFailed struct {
	RunID, NodeID, ExecID string
	Reason                FailReason
	Message               string
}

func (NodeExecutionFailed) EventType() string { return "node.execution.failed" }
func (NodeExecutionFailed) isEvent()          {}

// NodeExecutionInterrupted is recorded BEFORE the daemon kills a node's
// process group, per the L05 milestone
// (TestEngine_ctrlCInterruptsThenResumes, 12-build-plan.md).
type NodeExecutionInterrupted struct {
	RunID, NodeID, ExecID string
}

func (NodeExecutionInterrupted) EventType() string { return "node.execution.interrupted" }
func (NodeExecutionInterrupted) isEvent()          {}

// NodeExecutionLost is one of the two verdicts the 06-durability.md
// recovery scan reaches for a NodeExecution the log says is Executing: its
// process could not be verified alive after a restart.
type NodeExecutionLost struct {
	RunID, NodeID, ExecID string
}

func (NodeExecutionLost) EventType() string { return "node.execution.lost" }
func (NodeExecutionLost) isEvent()          {}

// NodeExecutionAdopted is the other recovery verdict: a surviving process
// was re-attached to. Only reachable starting L06 (restartPolicy: adopt),
// but the state exists in the enum from L01 (see nodeexecution.go).
type NodeExecutionAdopted struct {
	RunID, NodeID, ExecID string
}

func (NodeExecutionAdopted) EventType() string { return "node.execution.adopted" }
func (NodeExecutionAdopted) isEvent()          {}

// HumanTaskCreated records that a Waiting(human) execution now has a task
// visible to the human queue. The typed decision object is L13's; here the
// task is opaque.
type HumanTaskCreated struct {
	RunID, NodeID, ExecID string
}

func (HumanTaskCreated) EventType() string { return "human.task.created" }
func (HumanTaskCreated) isEvent()          {}

// HumanTaskAnswered resolves a Waiting(human) execution.
type HumanTaskAnswered struct {
	RunID, NodeID, ExecID string
	Output                json.RawMessage
	SchemaValid           bool
}

func (HumanTaskAnswered) EventType() string { return "human.task.answered" }
func (HumanTaskAnswered) isEvent()          {}
