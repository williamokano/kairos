package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/williamokano/kairos/internal/domain"
)

func timerKey(runID, nodeID, execID string) string {
	return runID + "/" + nodeID + "/" + execID
}

// armTimer schedules CmdArmTimer's FireAt as an in-memory time.Timer — see
// engine.go's timers field doc comment for the persistence caveat this
// carries. A FireAt already in the past fires immediately rather than
// scheduling a zero/negative timer (time.AfterFunc treats a non-positive
// duration as "run now" too, but computing it explicitly keeps the
// immediate-vs-scheduled cases visibly distinct here).
func (e *Engine) armTimer(c domain.CmdArmTimer) {
	key := timerKey(c.RunID, c.NodeID, c.ExecID)
	delay := time.Until(c.FireAt)

	e.runCtxMu.Lock()
	ctx := e.runCtx
	e.runCtxMu.Unlock()
	if ctx == nil {
		// Armed before Start (Reconcile's own catch-up pass calls this
		// too, before runCtx exists) — use Background so the fire
		// callback can still run; Reconcile's caller owns making sure a
		// definitely-overdue timer resolves synchronously instead of
		// going through this path at all (see resolveWaitTimeoutIfDue).
		ctx = context.Background()
	}

	fire := func() {
		if err := e.resolveWaitTimeout(ctx, c.RunID, c.NodeID, c.ExecID, true); err != nil {
			e.log.Error("resolving wait timeout", "runID", c.RunID, "nodeID", c.NodeID, "execID", c.ExecID, "error", err)
		}
		e.timersMu.Lock()
		delete(e.timers, key)
		e.timersMu.Unlock()
	}

	t := time.AfterFunc(delay, fire)
	e.timersMu.Lock()
	if old, ok := e.timers[key]; ok {
		old.Stop()
	}
	e.timers[key] = t
	e.timersMu.Unlock()
}

func (e *Engine) stopAllTimers() {
	e.timersMu.Lock()
	defer e.timersMu.Unlock()
	for key, t := range e.timers {
		t.Stop()
		delete(e.timers, key)
	}
}

// resolveWaitTimeout appends node.wait.resolved{Outcome: timed-out} for
// (runID, nodeID, execID) and, when dispatchCmds is true, folds it through
// domain.Advance and dispatches the resulting Cmds — the exact live/
// Reconcile split resolveConversationWait (L14) already established, for
// the same reason: Reconcile runs before any shard exists to pick the
// event up off the live bus itself.
func (e *Engine) resolveWaitTimeout(ctx context.Context, runID, nodeID, execID string, dispatchCmds bool) error {
	state, ok, err := e.store.GetRunState(ctx, runID)
	if err != nil {
		return fmt.Errorf("loading run state for %s: %w", runID, err)
	}
	if !ok {
		return nil
	}

	ev := domain.NodeWaitResolved{RunID: runID, NodeID: nodeID, ExecID: execID, Outcome: domain.WaitTimedOut}
	if err := e.appendNext(ctx, runID, ev); err != nil {
		if errors.Is(err, domain.ErrIllegalTransition) {
			// Already resolved (matched, or a duplicate timer fire raced
			// a concurrent path) — nothing left to do.
			return nil
		}
		return err
	}
	if !dispatchCmds {
		return nil
	}

	definitionRef, err := e.firstEventDefinitionRef(ctx, runID)
	if err != nil {
		return fmt.Errorf("resolving definitionRef for %s: %w", runID, err)
	}
	_, cmds, err := domain.Advance(state, ev, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("folding node.wait.resolved: %w", err)
	}
	for _, cmd := range cmds {
		if err := e.dispatch(ctx, definitionRef, cmd); err != nil {
			return fmt.Errorf("dispatching timeout-resolved cmd: %w", err)
		}
	}
	return nil
}

// rearmOutstandingTimers is Reconcile/Start's catch-up pass over every
// non-terminal run's Waiting executions whose node declares a wait
// timeout: overdue ones resolve synchronously right here (dispatchCmds
// true, exactly like reconcileRun's own recoverLost precedent — nothing
// else will ever act on them otherwise), not-yet-due ones are re-armed as
// ordinary in-memory timers.
func (e *Engine) rearmOutstandingTimers(ctx context.Context) error {
	runs, err := e.store.ListRuns(ctx, nil)
	if err != nil {
		return fmt.Errorf("listing runs: %w", err)
	}
	now := time.Now()
	for _, r := range runs {
		if r.Status.Terminal() {
			continue
		}
		state, ok, err := e.store.GetRunState(ctx, r.RunID)
		if err != nil {
			return fmt.Errorf("loading state for %s: %w", r.RunID, err)
		}
		if !ok {
			continue
		}
		for nodeID, execs := range state.Executions {
			if len(execs) == 0 {
				continue
			}
			exec := execs[len(execs)-1]
			if exec.Status != domain.ExecWaiting {
				continue
			}
			node, ok := state.Graph.NodeByID(nodeID)
			if !ok || node.Wait == nil || node.Wait.TimeoutAt == nil {
				continue
			}
			c := domain.CmdArmTimer{RunID: r.RunID, NodeID: string(nodeID), ExecID: exec.ExecID, FireAt: *node.Wait.TimeoutAt}
			if node.Wait.TimeoutAt.Before(now) {
				if err := e.resolveWaitTimeout(ctx, r.RunID, c.NodeID, c.ExecID, true); err != nil {
					return fmt.Errorf("resolving overdue wait timeout for %s/%s: %w", r.RunID, c.NodeID, err)
				}
				continue
			}
			e.armTimer(c)
		}
	}
	return nil
}
