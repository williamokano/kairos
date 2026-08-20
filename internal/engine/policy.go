package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/policy"
	"github.com/williamokano/kairos/internal/registry"
)

// checkEffects implements 05-gates.md's effect-permission tiers for
// nd's declared Effects, before c is admitted or dispatched to any actor.
// Returns blocked=true when the node has already been failed (deny, or a
// missing confirmation) and dispatchStartNode must stop — matching
// denyNode's own "nothing to unwind" property, since this runs before
// admission ever grants a claim.
//
// Scope, documented in full in L11-policy-secrets.md's Documented
// decisions: this is a SYNCHRONOUS check, not 05-gates.md's full
// pause-the-run-and-resume-on-a-human-answer flow. A confirm-tier effect
// requires an EffectConfirmed fact to ALREADY exist for
// (RunID, NodeID, Effect); if none does, the node fails immediately with
// an actionable reason (after recording EffectConfirmationRequested for
// audit) rather than parking indefinitely. The full async flow needs a
// new "waiting to start" state-machine addition to internal/domain that
// materially overlaps L12 (effects + compensation, which owns real effect
// dispatch) and L13 (the human queue) — Future work, not built here.
func (e *Engine) checkEffects(ctx context.Context, c domain.CmdStartNode, nd registry.NodeDef) (blocked bool, err error) {
	for _, effect := range nd.Effects {
		decision := e.policy.Decide(effect)
		switch decision.Tier {
		case policy.Allow:
			continue
		case policy.Deny:
			return true, e.denyNodeWithReason(ctx, c, domain.FailPolicyDenied, fmt.Sprintf("effect %q denied by policy: %s", effect, decision.Reason))
		case policy.Confirm:
			confirmed, err := e.effectConfirmed(ctx, c.RunID, c.NodeID, effect)
			if err != nil {
				return false, err
			}
			if confirmed {
				continue
			}
			if err := e.appendNext(ctx, c.RunID, domain.EffectConfirmationRequested{
				RunID: c.RunID, NodeID: c.NodeID, ExecID: c.ExecID, Effect: effect,
			}); err != nil {
				return false, err
			}
			return true, e.denyNodeWithReason(ctx, c, domain.FailPolicyDenied, fmt.Sprintf(
				"effect %q requires confirmation and none is recorded (record one via GrantEffectConfirmation before re-running)", effect))
		default:
			return true, e.denyNodeWithReason(ctx, c, domain.FailPolicyDenied, fmt.Sprintf("effect %q: unknown policy tier %q", effect, decision.Tier))
		}
	}
	return false, nil
}

// effectConfirmed reports whether an EffectConfirmed fact already exists
// for (runID, nodeID, effect) in the run's own stream.
func (e *Engine) effectConfirmed(ctx context.Context, runID, nodeID, effect string) (bool, error) {
	envs, err := e.store.Read(ctx, runID)
	if err != nil {
		return false, fmt.Errorf("reading stream %s: %w", runID, err)
	}
	for _, env := range envs {
		ev, ok := env.Event.(domain.EffectConfirmed)
		if ok && ev.NodeID == nodeID && ev.Effect == effect {
			return true, nil
		}
	}
	return false, nil
}

// GrantEffectConfirmation records EffectConfirmed for (runID, nodeID,
// effect) — the only way this document's synchronous confirm-tier check
// (checkEffects) is ever satisfied. A thin daemon API/CLI verb wrapping
// this call is Future work (see L11-policy-secrets.md); this is the
// callable core.
func (e *Engine) GrantEffectConfirmation(ctx context.Context, runID, nodeID, effect, scope string) error {
	return e.appendNext(ctx, runID, domain.EffectConfirmed{RunID: runID, NodeID: nodeID, Effect: effect, Scope: scope})
}

// GrantWaiver is the one and only code path that can append
// WaiverGranted — 05-gates.md's "waiver.grant is deny-tier for every
// non-human principal" made a real, testable invariant: actor must be
// exactly "human", checked here before the event exists at all, not
// merely documented. A thin daemon API/CLI verb wrapping this call is
// Future work (see L11-policy-secrets.md); this is the callable core.
func (e *Engine) GrantWaiver(ctx context.Context, actor, runID, nodeID, gateID, reason string, expiresAt time.Time) error {
	if actor != "human" {
		return fmt.Errorf("engine: waiver.grant is deny-tier for every non-human principal (got actor %q)", actor)
	}
	if reason == "" {
		return fmt.Errorf("engine: waiver.grant requires a reason")
	}
	return e.appendNext(ctx, runID, domain.WaiverGranted{
		RunID: runID, NodeID: nodeID, GateID: gateID, Reason: reason, ExpiresAt: expiresAt, GrantedBy: actor,
	})
}

// waiverActive reports whether a still-valid WaiverGranted exists for
// (runID, nodeID, gateID) — consulted only for gates already declared
// waivable: true (gates.go), so a waiver aimed at a waivable: false gate
// is never even looked up.
func (e *Engine) waiverActive(ctx context.Context, runID, nodeID, gateID string, now time.Time) (bool, error) {
	envs, err := e.store.Read(ctx, runID)
	if err != nil {
		return false, fmt.Errorf("reading stream %s: %w", runID, err)
	}
	for i := len(envs) - 1; i >= 0; i-- {
		ev, ok := envs[i].Event.(domain.WaiverGranted)
		if ok && ev.NodeID == nodeID && ev.GateID == gateID {
			return now.Before(ev.ExpiresAt), nil
		}
	}
	return false, nil
}
