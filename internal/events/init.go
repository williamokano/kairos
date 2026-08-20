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
