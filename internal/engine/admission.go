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

// storeClaim records a Granted decision's claims against execID, so the
// actor dispatch (running on a different goroutine once a reaper is
// spawned) can release them without needing the Claims value threaded
// through every actor function's signature.
func (e *Engine) storeClaim(execID string, claims admission.Claims) {
	e.claimsMu.Lock()
	e.claims[execID] = claims
	e.claimsMu.Unlock()
}

// drainedItem is one pending CmdStartNode that drainMu's decision phase
// resolved to a terminal admission outcome (Granted or Denied — never
// Queued, which just stops the drain).
type drainedItem struct {
	definitionRef string
	cmd           domain.CmdStartNode
	nd            registry.NodeDef
	actor         string
	claims        admission.Claims
	granted       bool
	denyReason    string
}

// releaseAndDrain releases execID's admission claims (a no-op if already
// released or never held — see admission.Manager.Release's doc comment),
// decides outcomes for every currently-queued CmdStartNode in FIFO order
// under drainMu, then dispatches those outcomes OUTSIDE the lock.
//
// Release and the decision phase run under the SAME drainMu critical
// section as admitOrQueue's decide-then-maybe-enqueue sequence
// (dispatch.go) — this closes a lost-wakeup race a real
// retry-onto-the-same-workspace scenario hit: without it, a concurrent
// dispatch could observe TryAdmit's Queued verdict, then lose the race to
// append itself to the pending list until AFTER this method's own drain
// had already run out and found the list empty — release happened, but
// nothing was left to wake the item that was about to queue itself, and
// it never ran again. Locking Release itself (not just the decision loop)
// closes that window: nothing can decide Queued-and-enqueue while a
// release is in flight, and nothing can release while a decision is being
// recorded.
//
// Dispatch (actor spawn, or denyNode's fold) happens only after drainMu
// is released, deliberately: a synchronous dispatch failure calls this
// same method again (dispatchLLMActor's own "if !spawned" defer, for one)
// — re-acquiring drainMu from inside a call already holding it would
// deadlock. Deciding first and dispatching after is what makes that
// reentrant call safe.
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

	e.drainMu.Lock()
	if ok {
		e.admit.Release(claims)
	}
	items := e.decidePendingLocked()
	e.drainMu.Unlock()

	e.dispatchDrained(ctx, items)
}

// admitOrQueue is dispatchStartNode's admission step: TryAdmit, and if
// the verdict is Queued, append to the pending list — both under drainMu,
// for the same reason releaseAndDrain takes it (see that method's doc
// comment). Returns the decision so the caller still handles
// Granted/Denied itself (dispatchStartNode's own caller dispatches
// immediately, on its own goroutine — only the drain path needs the
// decide/dispatch split, since only it can race a release).
func (e *Engine) admitOrQueue(definitionRef string, c domain.CmdStartNode, req admission.Request) admission.Decision {
	e.drainMu.Lock()
	defer e.drainMu.Unlock()
	decision := e.admit.TryAdmit(req)
	if decision.Outcome == admission.Queued {
		e.pendingMu.Lock()
		e.pending = append(e.pending, pendingStart{definitionRef: definitionRef, cmd: c})
		e.pendingMu.Unlock()
	}
	return decision
}

// decidePendingLocked resolves the head of the pending queue until it
// hits a request that cannot be admitted yet, or the queue empties,
// popping and collecting a terminal outcome for each — never dispatching
// anything itself (see releaseAndDrain's doc comment for why). Assumes
// drainMu is already held.
func (e *Engine) decidePendingLocked() []drainedItem {
	var out []drainedItem
	for {
		e.pendingMu.Lock()
		if len(e.pending) == 0 {
			e.pendingMu.Unlock()
			return out
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
			out = append(out, drainedItem{definitionRef: next.definitionRef, cmd: next.cmd, nd: nd, actor: actor, claims: decision.Claims, granted: true})
			// Loop again: draining one success may have freed nothing new,
			// but a Denied/Queued item further down never gets a chance if
			// we stop here only because this one succeeded.
		case admission.Denied:
			e.popPending()
			out = append(out, drainedItem{cmd: next.cmd, denyReason: decision.Reason})
		case admission.Queued:
			// Still can't run — stop draining, preserving FIFO order.
			return out
		}
	}
}

// dispatchDrained runs decidePendingLocked's collected outcomes AFTER
// drainMu has been released — see releaseAndDrain's doc comment for why
// dispatch must never run while the lock is held.
func (e *Engine) dispatchDrained(ctx context.Context, items []drainedItem) {
	for _, it := range items {
		if !it.granted {
			if err := e.denyNode(ctx, it.cmd, it.denyReason); err != nil {
				e.log.Error("recording denial for a drained admission failed", "runID", it.cmd.RunID, "err", err)
			}
			continue
		}
		e.storeClaim(it.cmd.ExecID, it.claims)
		if err := e.runActorDispatch(ctx, it.nd, it.cmd, it.actor); err != nil {
			e.log.Error("dispatch failed for a drained admission", "runID", it.cmd.RunID, "err", err)
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
