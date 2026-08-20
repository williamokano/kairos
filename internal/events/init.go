package events

import (
	"embed"
	"fmt"

	"github.com/williamokano/kairos/internal/domain"
)

//go:embed schemas
var schemaFS embed.FS

// builtinEvent names one (event_type, version) pair to register: the schema
// file under schemas/<eventType>/v<version>.json and a constructor for a
// fresh zero-value domain.Event.
type builtinEvent struct {
	eventType string
	version   int
	zero      ZeroFunc
}

// builtins is the append-only catalogue of every domain.Event type L01
// introduced. Adding a new version of an existing type means adding a new
// entry here (and a new schema file) — never editing an existing one
// (AGENTS §4 rule 6).
var builtins = []builtinEvent{
	{"trigger.received", 1, func() domain.Event { return &domain.TriggerReceived{} }},
	{"run.started", 1, func() domain.Event { return &domain.RunStarted{} }},
	{"run.rejected", 1, func() domain.Event { return &domain.RunRejected{} }},
	{"run.cancelled", 1, func() domain.Event { return &domain.RunCancelled{} }},
	{"run.degraded", 1, func() domain.Event { return &domain.RunDegraded{} }},
	{"run.degraded.resolved", 1, func() domain.Event { return &domain.RunDegradedResolved{} }},
	{"node.execution.started", 1, func() domain.Event { return &domain.NodeExecutionStarted{} }},
	{"node.output.received", 1, func() domain.Event { return &domain.NodeOutputReceived{} }},
	{"node.wait.resolved", 1, func() domain.Event { return &domain.NodeWaitResolved{} }},
	// v2 (L17): Outcome gains "failed" for a spawn/join's onChildFailure:
	// fail resolution — the schema-only change AGENTS §4 rule 6 permits
	// via a new version rather than editing v1's schema in place.
	{"node.wait.resolved", 2, func() domain.Event { return &domain.NodeWaitResolved{} }},
	{"node.gates.evaluated", 1, func() domain.Event { return &domain.NodeGatesEvaluated{} }},
	{"node.execution.failed", 1, func() domain.Event { return &domain.NodeExecutionFailed{} }},
	{"node.execution.interrupted", 1, func() domain.Event { return &domain.NodeExecutionInterrupted{} }},
	{"node.execution.lost", 1, func() domain.Event { return &domain.NodeExecutionLost{} }},
	{"node.execution.adopted", 1, func() domain.Event { return &domain.NodeExecutionAdopted{} }},
	{"human.task.created", 1, func() domain.Event { return &domain.HumanTaskCreated{} }},
	{"human.task.answered", 1, func() domain.Event { return &domain.HumanTaskAnswered{} }},
	// L05-introduced, additive, system-stream events (event.go's doc
	// comment on them explains why they have no RunID).
	{"engine.started", 1, func() domain.Event { return &domain.EngineStarted{} }},
	{"engine.stopped", 1, func() domain.Event { return &domain.EngineStopped{} }},
	{"engine.reconciled", 1, func() domain.Event { return &domain.EngineReconciled{} }},
	{"process.orphan.reaped", 1, func() domain.Event { return &domain.ProcessOrphanReaped{} }},
	// L16-introduced, system-stream: a TaskSource plugin call that
	// received a declared secret.
	{"secret.accessed", 1, func() domain.Event { return &domain.SecretAccessed{} }},
	// L08-introduced, run-scoped actor-invocation facts (event.go's doc
	// comments): unlike the four above, these DO have a RunID and ARE
	// folded through domain.Advance, as no-op audit transitions.
	{"llm.session.started", 1, func() domain.Event { return &domain.LLMSessionStarted{} }},
	{"session.resume.failed", 1, func() domain.Event { return &domain.SessionResumeFailed{} }},
	{"session.cost.unavailable", 1, func() domain.Event { return &domain.SessionCostUnavailable{} }},
	{"output.repair.attempted", 1, func() domain.Event { return &domain.OutputRepairAttempted{} }},
	// L09-introduced, run-scoped log-backpressure facts (event.go's doc
	// comments) — same posture as the L08 row above: RunID-bearing,
	// folded through domain.Advance as no-op audit transitions.
	{"log.degraded", 1, func() domain.Event { return &domain.LogDegraded{} }},
	{"log.truncated", 1, func() domain.Event { return &domain.LogTruncated{} }},
	// L10-introduced, run-scoped per-gate audit fact (event.go's doc
	// comment) — same posture as the two rows above.
	{"constraint.evaluated", 1, func() domain.Event { return &domain.ConstraintEvaluated{} }},
	// L11-introduced, run-scoped waiver/effect-confirmation facts
	// (event.go's doc comments) — same posture as the rows above.
	{"waiver.grant", 1, func() domain.Event { return &domain.WaiverGranted{} }},
	{"effect.confirmation.requested", 1, func() domain.Event { return &domain.EffectConfirmationRequested{} }},
	{"effect.confirmed", 1, func() domain.Event { return &domain.EffectConfirmed{} }},
	// L14-introduced, Conversation-stream-scoped fact (event.go's doc
	// comment) — same non-run-scoped posture as the L05 system-stream
	// rows above: no RunID, never folded through domain.Advance.
	{"conversation.message.appended", 1, func() domain.Event { return &domain.ConversationMessageAppended{} }},
	// L12-introduced, run-scoped effect state-machine facts (event.go's
	// doc comments) — same audit-transition posture as the rows above.
	{"effect.attempted", 1, func() domain.Event { return &domain.EffectAttempted{} }},
	{"effect.applied", 1, func() domain.Event { return &domain.EffectApplied{} }},
	{"effect.failed", 1, func() domain.Event { return &domain.EffectFailed{} }},
	{"effect.unknown", 1, func() domain.Event { return &domain.EffectUnknown{} }},
	{"effect.simulated", 1, func() domain.Event { return &domain.EffectSimulated{} }},
	{"effect.compensated", 1, func() domain.Event { return &domain.EffectCompensated{} }},
	{"effect.confirmation.parked", 1, func() domain.Event { return &domain.EffectConfirmationParked{} }},
	{"effect.confirmation.answered", 1, func() domain.Event { return &domain.EffectConfirmationAnswered{} }},
	// L17-introduced, run-scoped spawn/join bookkeeping facts
	// (event.go's doc comments) — same audit-transition posture as the
	// effect.* rows above.
	{"child.runs.planned", 1, func() domain.Event { return &domain.ChildRunsPlanned{} }},
	{"child.run.spawned", 1, func() domain.Event { return &domain.ChildRunSpawned{} }},
	// L18-introduced, run-scoped fork/snapshot bookkeeping facts
	// (event.go's doc comments) — same audit-transition posture as the
	// child.* rows above.
	{"workspace.snapshot.taken", 1, func() domain.Event { return &domain.WorkspaceSnapshotTaken{} }},
	{"run.forked", 1, func() domain.Event { return &domain.RunForked{} }},
	{"fork.workspace.drifted", 1, func() domain.Event { return &domain.ForkWorkspaceDrifted{} }},
}

// Builtin returns a Registry with every domain.Event type L01 defined,
// compiled from the schemas embedded under schemas/. A pure constructor
// call, not a package-level var (AGENTS §3: no package-level mutable
// state, no init() side effects).
func Builtin() (*Registry, error) {
	r := NewRegistry()
	for _, b := range builtins {
		path := fmt.Sprintf("schemas/%s/v%d.json", b.eventType, b.version)
		schemaJSON, err := schemaFS.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading embedded schema %s: %w", path, err)
		}
		if err := r.Register(b.eventType, b.version, schemaJSON, b.zero); err != nil {
			return nil, fmt.Errorf("registering %s v%d: %w", b.eventType, b.version, err)
		}
	}
	return r, nil
}
