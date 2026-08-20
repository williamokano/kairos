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
	// OutputRef is set instead of Output when the actor's output exceeds
	// 06-durability.md's 8 KiB inlining threshold — the engine stores the
	// full body in the content-addressed artifact store and records only
	// its reference here (L09). Output and OutputRef are never both set:
	// an oversized output is a reference, never a truncated inline copy.
	OutputRef *ArtifactRef
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

// LLMSessionStarted records a Kairos Session's identity — our own ULID,
// never the CLI's native session id (04-agents.md: "two concepts, never
// conflated"). Recorded before the actor process starts, exactly like
// NodeExecutionStarted precedes the process it names, so a Kairos Session
// is always traceable even if the process never produces output.
type LLMSessionStarted struct {
	RunID, NodeID, ExecID string
	SessionID             string
	// Resumed is true when this session reused a prior attempt's
	// conversation (native resume) rather than starting fresh.
	Resumed bool
	// Dir is this session's working directory — recorded so a later
	// attempt can detect 04-agents.md's "path-keying trap": the CLI's own
	// session store is keyed by cwd, so if Dir no longer exists a resume
	// would silently find nothing. Recorded now, checked before the next
	// attempt ever tries to resume.
	Dir string
}

func (LLMSessionStarted) EventType() string { return "llm.session.started" }
func (LLMSessionStarted) isEvent()          {}

// SessionResumeFailed records the path-keying trap 04-agents.md names
// explicitly: a resume was going to be attempted but the recorded resume
// directory no longer exists, so resuming would silently find nothing.
// Recorded instead of attempting it, and the engine falls back to a fresh
// session rather than guessing.
type SessionResumeFailed struct {
	RunID, NodeID, ExecID string
	PriorSessionID        string
}

func (SessionResumeFailed) EventType() string { return "session.resume.failed" }
func (SessionResumeFailed) isEvent()          {}

// SessionCostUnavailable records 04-agents.md's third cost-accounting
// tier: the actor reported no usable cost figure, so cost is recorded as
// 0 rather than estimated — "a made-up number in a budget check is worse
// than a missing one."
type SessionCostUnavailable struct {
	RunID, NodeID, ExecID string
}

func (SessionCostUnavailable) EventType() string { return "session.cost.unavailable" }
func (SessionCostUnavailable) isEvent()          {}

// OutputRepairAttempted records 04-agents.md's Stage 2 repair turn: the
// actor's first output failed schema validation, so the validation errors
// were fed back in the same session for one bounded repair attempt.
// Recorded regardless of whether the repair succeeded — the eventual
// NodeOutputReceived carries that outcome.
type OutputRepairAttempted struct {
	RunID, NodeID, ExecID string
	Errors                []string
}

func (OutputRepairAttempted) EventType() string { return "output.repair.attempted" }
func (OutputRepairAttempted) isEvent()          {}

// ArtifactRef points at a blob in the content-addressed artifact store
// (internal/artifact) instead of an inlined payload — recorded when a
// value would exceed 06-durability.md's 8 KiB inlining threshold (diffs,
// transcripts, large node outputs), keeping event payloads well under the
// event store's 64 KiB hard cap.
type ArtifactRef struct {
	Hash string
	Size int64
}

// LogDegraded records 06-durability.md's log backpressure policy applied
// at node-completion collection: rotation/compression of a node's log
// could not proceed safely (e.g. a write failure), so the log stream was
// left in its prior state rather than risking a silent gap — "block
// first, degrade second, never silently."
type LogDegraded struct {
	RunID, NodeID, ExecID string
	Stream                string // "stdout" or "stderr"
	Reason                string
}

func (LogDegraded) EventType() string { return "log.degraded" }
func (LogDegraded) isEvent()          {}

// LogTruncated records that a node's log stream was truncated rather than
// retained in full — recorded, never a silent gap (AGENTS §4 rule 1).
type LogTruncated struct {
	RunID, NodeID, ExecID string
	Stream                string
	DroppedBytes          int64
}

func (LogTruncated) EventType() string { return "log.truncated" }
func (LogTruncated) isEvent()          {}

// The four events below are additive, L05-introduced facts recorded to a
// separate "system" stream, not any run's stream — they have no RunID and
// Advance never folds them (RunState has no case for them; nothing calls
// Advance with one). Event only requires EventType()/isEvent(), neither of
// which mandates a RunID, so this is a pure addition, not a change to any
// existing type (AGENTS.md §4 rule 6).

// EngineStarted records a daemon boot, appended before reconciliation runs.
type EngineStarted struct {
	BootID string
}

func (EngineStarted) EventType() string { return "engine.started" }
func (EngineStarted) isEvent()          {}

// EngineStopped records a clean daemon shutdown — its absence as the last
// system-stream event is how the next boot detects an unclean exit
// (06-durability.md).
type EngineStopped struct{}

func (EngineStopped) EventType() string { return "engine.stopped" }
func (EngineStopped) isEvent()          {}

// EngineReconciled records that startup reconciliation finished — the
// readiness gate: the daemon does not start serving the API until this
// event exists in the system stream (09-cli-and-tui.md: "readiness flips
// only after engine.reconciled appears").
type EngineReconciled struct {
	Recovered     int
	Lost          int
	OrphansReaped int
}

func (EngineReconciled) EventType() string { return "engine.reconciled" }
func (EngineReconciled) isEvent()          {}

// ProcessOrphanReaped records a process group found alive at boot with no
// owning non-terminal NodeExecution, killed by reconciliation's orphan scan.
type ProcessOrphanReaped struct {
	PGID int
}

func (ProcessOrphanReaped) EventType() string { return "process.orphan.reaped" }
func (ProcessOrphanReaped) isEvent()          {}
