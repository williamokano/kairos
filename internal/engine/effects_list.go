package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/williamokano/kairos/internal/domain"
)

// EffectSummary is one effect action recorded against a run — what
// 05-gates.md's "kairos run effects <run>" names as "what has been
// applied and what compensation would unwind." The daemon-side data
// already exists in the event log (compensateRun, L12, walks the exact
// same shape to decide what to reverse on cancel/failure); this is the
// first read-only surface exposing it.
type EffectSummary struct {
	NodeID, ExecID, Effect string
	// Outcome is "applied" | "failed" | "unknown" | "attempted" (recorded
	// but not yet resolved — a live in-flight attempt, distinct from
	// "unknown", which means a restart's Probe genuinely could not tell).
	Outcome     string
	ExternalRef string
	Reason      string
	// Compensated is true once a matching EffectCompensated exists —
	// compensateRun will never touch this one again on a future
	// cancel/failure, it already ran (or was already reversed).
	Compensated bool
	// WouldCompensateOnCancel is true for exactly the set compensateRun
	// selects today: Outcome == "applied" and not yet Compensated.
	// Whether the provider can ACTUALLY reverse it (effect.
	// ErrNotCompensable is a real, live-only outcome) is not probed here
	// — probing would mean invoking Compensate informationally, which is
	// itself an external side effect this read-only listing must not
	// cause.
	WouldCompensateOnCancel bool
}

// Effects lists every effect action recorded in runID's stream, in the
// order they were attempted.
func (e *Engine) Effects(ctx context.Context, runID string) ([]EffectSummary, error) {
	envs, err := e.store.Read(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("reading stream %s: %w", runID, err)
	}

	order := []string{} // nodeID+"/"+execID+"/"+effect, first-seen order
	byKey := map[string]*EffectSummary{}
	key := func(nodeID, execID, eff string) string { return nodeID + "/" + execID + "/" + eff }

	for _, env := range envs {
		switch ev := env.Event.(type) {
		case domain.EffectAttempted:
			k := key(ev.NodeID, ev.ExecID, ev.Effect)
			if _, ok := byKey[k]; !ok {
				order = append(order, k)
			}
			byKey[k] = &EffectSummary{NodeID: ev.NodeID, ExecID: ev.ExecID, Effect: ev.Effect, Outcome: "attempted"}
		case domain.EffectApplied:
			k := key(ev.NodeID, ev.ExecID, ev.Effect)
			s := byKey[k]
			if s == nil {
				s = &EffectSummary{NodeID: ev.NodeID, ExecID: ev.ExecID, Effect: ev.Effect}
				order = append(order, k)
				byKey[k] = s
			}
			s.Outcome = "applied"
			s.ExternalRef = ev.ExternalRef
			s.WouldCompensateOnCancel = true
		case domain.EffectFailed:
			k := key(ev.NodeID, ev.ExecID, ev.Effect)
			s := byKey[k]
			if s == nil {
				s = &EffectSummary{NodeID: ev.NodeID, ExecID: ev.ExecID, Effect: ev.Effect}
				order = append(order, k)
				byKey[k] = s
			}
			s.Outcome = "failed"
			s.Reason = ev.Reason
		case domain.EffectUnknown:
			k := key(ev.NodeID, ev.ExecID, ev.Effect)
			s := byKey[k]
			if s == nil {
				s = &EffectSummary{NodeID: ev.NodeID, ExecID: ev.ExecID, Effect: ev.Effect}
				order = append(order, k)
				byKey[k] = s
			}
			s.Outcome = "unknown"
		case domain.EffectCompensated:
			k := key(ev.NodeID, ev.ExecID, ev.Effect)
			if s := byKey[k]; s != nil {
				s.Compensated = true
				s.WouldCompensateOnCancel = false
			}
		}
	}

	out := make([]EffectSummary, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k]) // order is already first-seen — no re-sort needed
	}
	return out, nil
}

// ResolveEffectUnknown lets an operator manually resolve a node blocked
// in effect.unknown — 05-gates.md's "an operator cannot yet resolve a
// blocked effect.unknown node without direct event-store access," until
// now. outcome is "applied" or "failed"; for "applied", externalRef may
// be empty (the whole reason this node is stuck is that the provider
// itself couldn't determine one — an operator confirming "yes, it went
// through" from firsthand knowledge, e.g. having checked GitHub
// directly, may have nothing better to record than the reason string).
// This is exactly reconcileEffectNode's own res.Outcome switch (effect.go),
// made reachable outside of Reconcile's automatic probing.
func (e *Engine) ResolveEffectUnknown(ctx context.Context, runID, nodeID, outcome, externalRefOrReason string) error {
	state, ok, err := e.store.GetRunState(ctx, runID)
	if err != nil {
		return fmt.Errorf("loading run state for %s: %w", runID, err)
	}
	if !ok {
		return fmt.Errorf("no such run: %s", runID)
	}
	execs := state.Executions[domain.NodeID(nodeID)]
	var target *domain.NodeExecution
	for i := range execs {
		if execs[i].Status == domain.ExecExecuting {
			target = &execs[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("node %q has no Executing execution to resolve — it may already be resolved, or was never blocked on effect.unknown", nodeID)
	}

	envs, err := e.store.Read(ctx, runID)
	if err != nil {
		return fmt.Errorf("reading stream %s: %w", runID, err)
	}
	var attempted *domain.EffectAttempted
	for _, env := range envs {
		if a, ok := env.Event.(domain.EffectAttempted); ok && a.ExecID == target.ExecID {
			attempted = &a
		}
	}
	if attempted == nil {
		return fmt.Errorf("node %q's current execution %s has no recorded effect.attempted — nothing to resolve", nodeID, target.ExecID)
	}

	var resultEv domain.Event
	var followupEv domain.Event
	switch outcome {
	case "applied":
		resultEv = domain.EffectApplied{RunID: runID, NodeID: nodeID, ExecID: target.ExecID, Effect: attempted.Effect, ExternalRef: externalRefOrReason}
		out, err := json.Marshal(map[string]any{"externalRef": externalRefOrReason, "resolvedManually": true})
		if err != nil {
			return fmt.Errorf("marshalling effect output: %w", err)
		}
		followupEv = domain.NodeOutputReceived{RunID: runID, NodeID: nodeID, ExecID: target.ExecID, SchemaValid: true, Output: json.RawMessage(out)}
	case "failed":
		resultEv = domain.EffectFailed{RunID: runID, NodeID: nodeID, ExecID: target.ExecID, Effect: attempted.Effect, Reason: externalRefOrReason}
		followupEv = domain.NodeExecutionFailed{RunID: runID, NodeID: nodeID, ExecID: target.ExecID, Reason: domain.FailFailure, Message: "resolved manually: " + externalRefOrReason}
	default:
		return fmt.Errorf("outcome must be \"applied\" or \"failed\", got %q", outcome)
	}

	if err := e.appendNext(ctx, runID, resultEv); err != nil {
		return err
	}
	if e.isLive() {
		return e.appendNext(ctx, runID, followupEv)
	}
	return e.appendAndFoldBeforeStart(ctx, runID, followupEv)
}
