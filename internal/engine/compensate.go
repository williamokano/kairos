package engine

import (
	"context"
	"sort"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/effect"
)

// compensateRun reverses every EffectApplied effect in runID's stream
// that has no matching EffectCompensated yet, in strict reverse
// application order (last-applied-first) — 05-gates.md's compensation
// contract for a run that ends Cancelled or Failed with real external
// state already created. Compensation is best-effort by nature: an
// effect the provider can't reverse (effect.ErrNotCompensable, or any
// other Compensate error) is logged and left applied rather than
// retried or treated as fatal — reversing an external mutation this
// system doesn't fully control is not a guarantee this document can
// make.
func (e *Engine) compensateRun(ctx context.Context, runID string) {
	envs, err := e.store.Read(ctx, runID)
	if err != nil {
		e.log.Error("compensateRun: reading stream failed", "runID", runID, "err", err)
		return
	}

	type applied struct {
		seq                    int
		nodeID, execID, effect string
		externalRef            string
	}
	var toCompensate []applied
	compensated := map[string]bool{} // nodeID+"/"+execID+"/"+effect

	for _, env := range envs {
		switch ev := env.Event.(type) {
		case domain.EffectApplied:
			toCompensate = append(toCompensate, applied{
				seq: env.Sequence, nodeID: ev.NodeID, execID: ev.ExecID, effect: ev.Effect, externalRef: ev.ExternalRef,
			})
		case domain.EffectCompensated:
			compensated[ev.NodeID+"/"+ev.ExecID+"/"+ev.Effect] = true
		}
	}

	// Reverse application order: last-applied-first.
	sort.Slice(toCompensate, func(i, j int) bool { return toCompensate[i].seq > toCompensate[j].seq })

	for _, a := range toCompensate {
		key := a.nodeID + "/" + a.execID + "/" + a.effect
		if compensated[key] {
			continue
		}
		provider, ok := e.effectProviders[a.effect]
		if !ok {
			e.log.Warn("compensateRun: no provider for effect, leaving applied", "runID", runID, "effect", a.effect)
			continue
		}
		workDir, err := e.workspaceRepoDirFor(ctx, runID)
		if err != nil {
			e.log.Warn("compensateRun: resolving workspace failed, leaving applied", "runID", runID, "effect", a.effect, "err", err)
			continue
		}
		req := effect.Request{
			RunID: runID, NodeID: a.nodeID, ExecID: a.execID, Effect: a.effect,
			WorkDir: workDir, Dir: e.scratchDir(runID, a.execID),
		}
		if err := provider.Compensate(ctx, req, a.externalRef); err != nil {
			e.log.Warn("compensateRun: compensation failed, leaving applied", "runID", runID, "effect", a.effect, "externalRef", a.externalRef, "err", err)
			continue
		}
		if err := e.appendNext(ctx, runID, domain.EffectCompensated{
			RunID: runID, NodeID: a.nodeID, ExecID: a.execID, Effect: a.effect, ExternalRef: a.externalRef,
		}); err != nil {
			e.log.Error("compensateRun: recording effect.compensated failed", "runID", runID, "err", err)
		}
	}
}
