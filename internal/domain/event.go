package domain

import (
	"encoding/json"
	"time"
)

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
	// WaitFailed is L17's addition: a spawn/join's children finished with
	// at least one failure under onChildFailure: fail (the default) — a
	// genuine failure, distinct from a schema problem or a timeout, so it
	// gets its own outcome rather than being squeezed into SchemaValid.
	WaitFailed WaitOutcome = "failed"
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
	// ResumeMode is "native" when Resumed is true (L14 makes the CLI's own
	// --resume/exec-resume flag a real part of the invocation, not just an
	// env-var hint) and empty otherwise. 04-agents.md's other named mode,
	// "digest" (rehydrate from run memory plus a summarised transcript),
	// is Future work — it needs context re-injection machinery this
	// document does not build (see L14-conversations.md).
	ResumeMode string
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

// ConstraintEvaluated is L10's per-gate audit fact: one evaluation of one
// declared gate, appended whether it passed or failed (05-gates.md's
// invariant clause 3 — "result appended before edges" — is satisfied by
// domain.NodeGatesEvaluated, the aggregate this event does NOT drive by
// itself; ConstraintEvaluated is evidence, not routing). Like L08's
// session/repair facts and L09's log-backpressure facts, it carries a
// RunID and is folded through domain.Advance as a no-op audit transition.
type ConstraintEvaluated struct {
	RunID, NodeID, ExecID string
	GateID                string
	Kind                  string // "expr" | "command"
	Passed                bool
	ExitCode              int // command kind only; 0 for expr
	DurationMs            int64
	Reason                string // human-readable verdict detail, always set
}

func (ConstraintEvaluated) EventType() string { return "constraint.evaluated" }
func (ConstraintEvaluated) isEvent()          {}

// WaiverGranted records L11's one and only way a waivable gate's failure
// stops blocking a run: a human-authored fact with a reason and an
// expiry, never something the engine or an actor can produce itself
// (05-gates.md: "waiver.grant is deny-tier for every non-human
// principal"). See internal/engine's GrantWaiver, the sole code path
// that appends this event, which rejects any non-"human" actor outright.
// A waiver targeting a Waivable: false gate is never consulted — L10's
// gates.go looks it up only for gates already declared waivable, so a
// waiver aimed at a non-waivable gate is simply never read, matching
// "false means no code path in this engine can mark this gate's failure
// as passed, full stop" (registry.GateDef's own doc comment).
type WaiverGranted struct {
	RunID, NodeID string
	GateID        string
	Reason        string
	ExpiresAt     time.Time
	GrantedBy     string // always "human" — GrantWaiver rejects any other value before this event exists
}

func (WaiverGranted) EventType() string { return "waiver.grant" }
func (WaiverGranted) isEvent()          {}

// EffectConfirmationRequested is L11's audit fact that a confirm-tier
// effect (~/.kairos/policy.yaml) blocked a node's dispatch pending a
// recorded EffectConfirmed. Unlike 05-gates.md's full flow — which parks
// the run, releases every permit, and resumes automatically once a
// human answers — this document's scope stops at a synchronous check:
// the node fails with a clear, actionable reason if no EffectConfirmed
// already exists for (RunID, NodeID, Effect) at dispatch time. The full
// pause-and-resume flow needs a new async-wait state-machine addition to
// internal/domain (a fourth "waiting to start" concept alongside
// ExecWaiting's existing wait-node-only usage) that materially overlaps
// L12 (effects + compensation, which owns real effect dispatch) and L13
// (the human queue) — see L11-policy-secrets.md's Documented decisions
// and Future work.
type EffectConfirmationRequested struct {
	RunID, NodeID, ExecID string
	Effect                string
}

func (EffectConfirmationRequested) EventType() string { return "effect.confirmation.requested" }
func (EffectConfirmationRequested) isEvent()          {}

// EffectConfirmed is the human-authored answer EffectConfirmationRequested
// waits on — recorded ahead of a node's dispatch (this document's
// synchronous-check scope, see EffectConfirmationRequested's doc comment),
// not the response to a live in-run prompt.
type EffectConfirmed struct {
	RunID, NodeID string
	Effect        string
	Scope         string // "once" | "run" (05-gates.md's effect.confirmed{scope})
}

func (EffectConfirmed) EventType() string { return "effect.confirmed" }
func (EffectConfirmed) isEvent()          {}

// EffectConfirmationParked is L12's real park transition — unlike
// EffectConfirmationRequested (an audit-only fact, kept for L11
// compatibility, still folded as a no-op), this event actually moves the
// still-Pending NodeExecution to ExecWaiting and dispatches
// CmdCreateHumanTask, matching 05-gates.md's "insert a human task →
// RELEASE ALL PERMITS → the node enters Waiting" — permits are trivially
// released because none were ever granted: checkEffects runs before
// admission (internal/engine/dispatch.go), so a parked node never held a
// claim to begin with.
type EffectConfirmationParked struct {
	RunID, NodeID, ExecID string
	Effect                string
}

func (EffectConfirmationParked) EventType() string { return "effect.confirmation.parked" }
func (EffectConfirmationParked) isEvent()          {}

// EffectConfirmationAnswered resolves a parked confirmation — Approved
// resumes the SAME NodeExecution (back to ExecPending, re-dispatched via
// CmdStartNode with its original ExecID/Attempt/Iteration, so the fresh
// EffectConfirmed fact checkEffects now finds lets it proceed to the
// actual effect); a decline routes via the node's failure edge, matching
// 05-gates.md's "n → effect.declined → on.denied" (this codebase has no
// distinct OnDenied edge trigger — reusing OnFailure, the same trigger
// checkEffects's Deny-tier path already uses for a policy denial).
type EffectConfirmationAnswered struct {
	RunID, NodeID, ExecID string
	Approved              bool
	Reason                string
}

func (EffectConfirmationAnswered) EventType() string { return "effect.confirmation.answered" }
func (EffectConfirmationAnswered) isEvent()          {}

// EffectAttempted is L12's "decision before action" record for a builtin
// effect (actor: effect — git.push, gh.pr.create): appended BEFORE the
// external call, so a crash mid-call leaves this as the last fact and
// startup reconciliation probes the effect by IdempotencyKey instead of
// blindly retrying (06-durability.md's "the single most valuable recovery
// path in the system"). IdempotencyKey is a pure function of
// (RunID, NodeID, Effect) — see internal/effect.IdempotencyKey and its
// doc comment on why lineage == RunID until L17 gives forking real
// meaning.
type EffectAttempted struct {
	RunID, NodeID, ExecID string
	Effect                string
	IdempotencyKey        string
}

func (EffectAttempted) EventType() string { return "effect.attempted" }
func (EffectAttempted) isEvent()          {}

// EffectApplied is the successful terminal outcome of an EffectAttempted
// effect — ExternalRef is the provider's own identifier for what it did
// (a PR URL, a pushed ref), the value reverse-order compensation reads to
// know what to undo.
type EffectApplied struct {
	RunID, NodeID, ExecID string
	Effect                string
	ExternalRef           string
}

func (EffectApplied) EventType() string { return "effect.applied" }
func (EffectApplied) isEvent()          {}

// EffectFailed is the failed terminal outcome: the provider ran and
// definitively knows the mutation did not happen.
type EffectFailed struct {
	RunID, NodeID, ExecID string
	Effect                string
	Reason                string
}

func (EffectFailed) EventType() string { return "effect.failed" }
func (EffectFailed) isEvent()          {}

// EffectUnknown is 06-durability.md's third, deliberately non-retryable
// outcome: reconciliation probed by IdempotencyKey and the provider
// itself could not say whether the mutation happened. Recording this
// does not resolve the owning NodeExecution to any terminal status — it
// stays Executing with no further NodeExecutionFailed/NodeOutputReceived
// fold, which is what "blocks the run reaching Failed" concretely means
// here: a RunState can never reach a terminal status while any
// NodeExecution stays non-terminal. Resolving it is an operator action
// (see internal/engine's ResolveEffectUnknown) this document exposes as
// a callable core function, with a thin CLI verb named as Future work.
type EffectUnknown struct {
	RunID, NodeID, ExecID string
	Effect                string
}

func (EffectUnknown) EventType() string { return "effect.unknown" }
func (EffectUnknown) isEvent()          {}

// EffectSimulated records a dry-run: the effect was previewed, not
// performed. See internal/engine.Config.DryRun's doc comment for this
// document's engine-wide (not yet per-run) scope.
type EffectSimulated struct {
	RunID, NodeID, ExecID string
	Effect                string
}

func (EffectSimulated) EventType() string { return "effect.simulated" }
func (EffectSimulated) isEvent()          {}

// EffectCompensated records that a previously EffectApplied effect was
// reversed — by internal/engine's compensateRun, in strict reverse
// application order, triggered when a run transitions to Cancelled or
// Failed with one or more effects already applied.
type EffectCompensated struct {
	RunID, NodeID, ExecID string
	Effect                string
	ExternalRef           string
}

func (EffectCompensated) EventType() string { return "effect.compensated" }
func (EffectCompensated) isEvent()          {}

// ConversationMessageAppended is one message on a Conversation's own
// stream (stream_id = "conversation:<runID>" — L14 scopes Conversation
// 1:1 with Run, see L14-conversations.md's Documented decisions), never
// folded by Advance: a Conversation is not run-scoped state, the same
// posture the "system" stream's events already hold since L05. Role is
// "human" for every message this document's scope produces (the composer
// in 09-cli-and-tui.md's mockup); the field exists for forward
// compatibility with an "actor"/"system" role a later document may add,
// not because anything writes one today.
type ConversationMessageAppended struct {
	Role string
	Text string
}

func (ConversationMessageAppended) EventType() string { return "conversation.message.appended" }
func (ConversationMessageAppended) isEvent()          {}

// SecretAccessed records one TaskSource plugin call that received a
// declared secret as an environment variable (08-triggers.md: "the
// engine refuses to start a plugin whose declared secrets are unset, and
// records secret.accessed{plugin, secret, callID} per call"). System-
// stream, never folded by Advance — a fact about the daemon's own
// operation, not about any run.
type SecretAccessed struct {
	Plugin string
	Secret string
	CallID string
}

func (SecretAccessed) EventType() string { return "secret.accessed" }
func (SecretAccessed) isEvent()          {}

// ChildPlanItem is one resolved forEach element a spawn: node will (or
// has) turned into a child run.
type ChildPlanItem struct {
	Index  int
	Params json.RawMessage
}

// ChildRunsPlanned records a spawn: node's forEach resolution — the full,
// fixed set of children it intends to create — once, at CmdSpawnChildren
// dispatch time (L17). Run-scoped, never folded by Advance (like
// ConstraintEvaluated): it is bookkeeping the engine and reconciliation
// read back from the log, not state routing cares about.
type ChildRunsPlanned struct {
	RunID, NodeID, ExecID string
	Items                 []ChildPlanItem
}

func (ChildRunsPlanned) EventType() string { return "child.runs.planned" }
func (ChildRunsPlanned) isEvent()          {}

// ChildRunSpawned records one planned item actually turned into a real
// child Run (L17) — the engine appends one of these per successful spawn,
// which is what lets reconciliation and the completion handler know which
// planned items are already spawned versus still queued under
// strategy: bounded(N)'s progressive refill.
type ChildRunSpawned struct {
	RunID, NodeID, ExecID string
	Index                 int
	ChildRunID            string
}

func (ChildRunSpawned) EventType() string { return "child.run.spawned" }
func (ChildRunSpawned) isEvent()          {}

// WorkspaceSnapshotTaken records one out-of-band git-ref snapshot of a
// workspace: write node's tree, taken at a node-completion boundary
// (L18, ADR 0006). Ref is the exact refs/kairos/runs/<runID>/<seq> name;
// SHA is the commit-tree object it points at. Run-scoped, never folded by
// Advance (bookkeeping the engine/Fork read back from the log, matching
// ChildRunsPlanned's posture) — Kind is "git" (this document's scope; a
// future "git+tree" value is reserved for when tree-level CoW capture is
// wired into this same event, per ADR 0006's two-layer design).
type WorkspaceSnapshotTaken struct {
	RunID, NodeID, ExecID string
	AtSequence            int
	Label                 string
	Kind                  string // "git" today; "git+tree" reserved
	Ref                   string
	SHA                   string
}

func (WorkspaceSnapshotTaken) EventType() string { return "workspace.snapshot.taken" }
func (WorkspaceSnapshotTaken) isEvent()          {}

// RunForked is the first event a forked run's stream carries after its
// copied event prefix (06-durability.md's "Fork and replay") — never
// folded by Advance (bookkeeping, same posture as WorkspaceSnapshotTaken),
// since the copied prefix has already fully re-established RunState by
// the time this is appended. LineageRoot is FromRunID's own lineage root
// (itself, if FromRunID was never forked) — internal/effect's
// IdempotencyKey uses it so a fork's effect actions update the lineage's
// external state rather than duplicating it.
type RunForked struct {
	RunID       string // the NEW run's id (this stream's own id)
	FromRunID   string
	LineageRoot string
	AtSequence  int
	Overrides   map[string]string
}

func (RunForked) EventType() string { return "run.forked" }
func (RunForked) isEvent()          {}

// ForkWorkspaceDrifted records that Fork proceeded past a missing
// snapshot only because --allow-drift was passed (06-durability.md:
// "--allow-drift snapshots now and records
// fork.workspace.drifted{requestedSeq, actualSeq}") — on the NEW run's
// own stream, since it is a fact about how that run came to exist.
type ForkWorkspaceDrifted struct {
	RunID        string
	RequestedSeq int
	ActualSeq    int
}

func (ForkWorkspaceDrifted) EventType() string { return "fork.workspace.drifted" }
func (ForkWorkspaceDrifted) isEvent()          {}
