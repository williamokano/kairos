package engine

import (
	"context"

	"github.com/williamokano/kairos/internal/admission"
	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/registry"
)

// llmActorKinds mirrors dispatch.go's actor switch — the set of actor
// names that spawn an LLM CLI and therefore claim a model-class slot
// (admission rule 4), not just a node slot.
var llmActorKinds = map[string]bool{"claude": true, "codex": true, "gemini": true, "local": true}

// admissionRequest builds one node execution's all-or-nothing claim ask
// from its resolved NodeDef/actor — the mapping from 03-workflows.md's
// authored fields to admission's pool keys.
func (e *Engine) admissionRequest(nd registry.NodeDef, actor string) admission.Request {
	req := admission.Request{NodeSlot: true}
	if nd.Workspace == registry.WorkspaceWrite {
		req.WorkspaceKey = e.workspaceRepo
	}
	if llmActorKinds[actor] {
		req.ModelClass = nd.Resources.ModelClass
	}
	req.EstimatedCostUSD = nd.Resources.MaxCostUSD
	e.pendingMu.Lock()
	req.QueueDepth = len(e.pending)
	e.pendingMu.Unlock()
	return req
}

// enqueuePending holds a Queued CmdStartNode for later retry — no side
// effect has been recorded for it yet (admission runs before
// NodeExecutionStarted), so replaying it later via runActorDispatch is
// exactly as if dispatchStartNode were being called for the first time.
func (e *Engine) enqueuePending(definitionRef string, c domain.CmdStartNode) {
	e.pendingMu.Lock()
	e.pending = append(e.pending, pendingStart{definitionRef: definitionRef, cmd: c})
	e.pendingMu.Unlock()
}

// storeClaim records a Granted decision's claims against execID, so the
// actor dispatch (running on a different goroutine once a reaper is
// spawned) can release them without needing the Claims value threaded
// through every actor function's signature.
func (e *Engine) storeClaim(execID string, claims admission.Claims) {
	e.claimsMu.Lock()
	e.claims[execID] = claims
	e.claimsMu.Unlock()
}

// releaseAndDrain releases execID's admission claims (a no-op if already
// released or never held — see admission.Manager.Release's doc comment)
// and then retries every currently-queued CmdStartNode in FIFO order,
// stopping at the first one that is still not admittable so ordering
// among queued nodes is preserved.
//
// Safe to call more than once for the same execID: the second call finds
// no claim in the map and only re-attempts the drain, which is harmless.
func (e *Engine) releaseAndDrain(ctx context.Context, execID string) {
	e.claimsMu.Lock()
	claims, ok := e.claims[execID]
	if ok {
		delete(e.claims, execID)
	}
	e.claimsMu.Unlock()
	if ok {
		e.admit.Release(claims)
	}
	e.drainPending(ctx)
}

// drainPending retries the head of the pending queue until it hits a
// request that cannot be admitted yet, or the queue empties. Draining one
// item at a time from the front, stopping on the first non-Granted
// outcome, keeps queued nodes released in the order they queued rather
// than allowing a later, cheaper request to jump ahead.
//
// drainMu serializes the entire method: releaseAndDrain runs from
// whichever goroutine finishes a node execution, so two releases can
// legitimately race each other. Without a single-drainer lock, both could
// read the same queue head before either pops it and admit the same
// CmdStartNode twice — pendingMu alone only protects individual
// read/pop/append operations, not the read-decide-pop sequence as a
// whole.
func (e *Engine) drainPending(ctx context.Context) {
	e.drainMu.Lock()
	defer e.drainMu.Unlock()

	for {
		e.pendingMu.Lock()
		if len(e.pending) == 0 {
			e.pendingMu.Unlock()
			return
		}
		next := e.pending[0]
		e.pendingMu.Unlock()

		def, err := e.loadDefinition(next.definitionRef)
		if err != nil {
			e.log.Error("draining pending admission: loading definition failed", "runID", next.cmd.RunID, "err", err)
			e.popPending()
			continue
		}
		nd, actor, found := resolveNodeActor(def, next.cmd)
		if !found {
			e.log.Error("draining pending admission: node not found", "runID", next.cmd.RunID, "nodeID", next.cmd.NodeID)
			e.popPending()
			continue
		}

		req := e.admissionRequest(nd, actor)
		decision := e.admit.TryAdmit(req)
		switch decision.Outcome {
		case admission.Granted:
			e.popPending()
			e.storeClaim(next.cmd.ExecID, decision.Claims)
			if err := e.runActorDispatch(ctx, nd, next.cmd, actor); err != nil {
				e.log.Error("dispatch failed for a drained admission", "runID", next.cmd.RunID, "err", err)
			}
			// Loop again: draining one success may have freed nothing new,
			// but a Denied/Queued item further down never gets a chance if
			// we stop here only because this one succeeded.
		case admission.Denied:
			e.popPending()
			if err := e.denyNode(ctx, next.cmd, decision.Reason); err != nil {
				e.log.Error("recording denial for a drained admission failed", "runID", next.cmd.RunID, "err", err)
			}
		case admission.Queued:
			// Still can't run — stop draining, preserving FIFO order.
			return
		}
	}
}

func (e *Engine) popPending() {
	e.pendingMu.Lock()
	if len(e.pending) > 0 {
		e.pending = e.pending[1:]
	}
	e.pendingMu.Unlock()
}

// resolveNodeActor mirrors dispatchStartNode's own node lookup plus
// retry.mutate resolution, factored out so drainPending can reuse it
// without duplicating the logic.
func resolveNodeActor(def registry.Definition, c domain.CmdStartNode) (registry.NodeDef, string, bool) {
	for _, n := range def.Nodes {
		if string(n.ID) != c.NodeID {
			continue
		}
		actor := n.Actor
		for _, m := range n.Retry.Mutate {
			if m.Attempt == c.Attempt && m.Actor != "" {
				actor = m.Actor
			}
		}
		return n, actor, true
	}
	return registry.NodeDef{}, "", false
}
