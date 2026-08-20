package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/williamokano/kairos/internal/conversation"
	"github.com/williamokano/kairos/internal/domain"
)

// resolveConversationWait implements 03-workflows.md's wait: conversation
// kind. A Waiting node "holds nothing" (06-durability.md) — no goroutine,
// no waiter row — so resolution is fully re-derivable from the log: is
// runID currently at an ExecWaiting NodeExecution whose node declares
// wait.on[0].kind == conversation? If so, the Conversation's latest
// message is what it was waiting for.
//
// dispatchCmds distinguishes the two callers' very different situations.
// Live (engine.go's Start loop, on every new conversation.message.appended)
// passes false: the NodeWaitResolved this appends lands on runID's own
// stream, which the live Subscribe loop is already watching, so the
// owning shard picks it up and dispatches the resulting CmdEvaluateGates
// itself — dispatching here too would run it twice. Reconcile passes
// true: it runs before any shard exists (no live loop has ever seen this
// event, and reconcileRun already sets this precedent for exactly this
// reason — see recoverLost's identical fold-then-dispatch-manually
// pattern for a Lost node's retry), so nothing else will ever act on the
// commands unless this call does.
func (e *Engine) resolveConversationWait(ctx context.Context, runID string, dispatchCmds bool) error {
	state, ok, err := e.store.GetRunState(ctx, runID)
	if err != nil {
		return fmt.Errorf("loading run state for %s: %w", runID, err)
	}
	if !ok {
		return nil // conversation predates (or outlives) any run with this id
	}

	nodeID, execID, ok := waitingOnConversation(state)
	if !ok {
		return nil
	}

	msgs, err := conversation.Messages(ctx, e.store, runID)
	if err != nil {
		return err
	}
	if len(msgs) == 0 {
		return nil
	}
	latest := msgs[len(msgs)-1]

	definitionRef, err := e.firstEventDefinitionRef(ctx, runID)
	if err != nil {
		return fmt.Errorf("resolving definitionRef for %s: %w", runID, err)
	}
	nd, ok := e.resolveNode(definitionRef, nodeID)
	if !ok {
		return fmt.Errorf("node %s not found in definition for run %s", nodeID, runID)
	}

	body, err := json.Marshal(map[string]string{"message": latest.Text})
	if err != nil {
		return fmt.Errorf("marshalling wait: conversation output: %w", err)
	}
	valid := validateOutput(nd, body)

	ev := domain.NodeWaitResolved{
		RunID: runID, NodeID: nodeID, ExecID: execID,
		Outcome: domain.WaitMatched, SchemaValid: valid, Output: json.RawMessage(body),
	}
	if err := e.appendNext(ctx, runID, ev); err != nil {
		if errors.Is(err, domain.ErrIllegalTransition) {
			// Already resolved by a concurrent caller (the live path and
			// a Reconcile catch-up pass can race harmlessly) — the exec
			// moved on since GetRunState was read above; nothing left to
			// do.
			return nil
		}
		return err
	}
	if !dispatchCmds {
		return nil
	}

	_, cmds, err := domain.Advance(state, ev, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("folding node.wait.resolved: %w", err)
	}
	for _, cmd := range cmds {
		if err := e.dispatch(ctx, definitionRef, cmd); err != nil {
			return fmt.Errorf("dispatching resolved-wait cmd: %w", err)
		}
	}
	return nil
}

// waitingOnConversation reports the (nodeID, execID) of state's Waiting
// NodeExecution whose node declares wait: conversation, if any. Only the
// LAST entry per node in state.Executions is current (a retry/iteration
// appends, never replaces — internal/domain's own withExecution does the
// same); a run has at most one Executing/Waiting node at a time in every
// workflow shape this engine dispatches (no fan-out yet — L17), so the
// first current match is unambiguous.
func waitingOnConversation(state domain.RunState) (nodeID, execID string, found bool) {
	for id, execs := range state.Executions {
		if len(execs) == 0 {
			continue
		}
		exec := execs[len(execs)-1]
		if exec.Status != domain.ExecWaiting {
			continue
		}
		node, ok := state.Graph.NodeByID(id)
		if !ok || node.Wait == nil || node.Wait.Kind != domain.WaitConversation {
			continue
		}
		return string(id), exec.ExecID, true
	}
	return "", "", false
}
