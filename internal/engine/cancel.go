package engine

import (
	"context"
	"fmt"

	"github.com/williamokano/kairos/internal/domain"
)

// ErrCancelReasonRequired mirrors ErrHumanDecisionReasonRequired's
// discipline (human.go): a destructive, irreversible-by-nature operation
// always records why, never a silent stop.
var ErrCancelReasonRequired = fmt.Errorf("engine: a reason is required to cancel a run")

// ErrRunAlreadyTerminal is returned when Cancel is asked to stop a run that
// has already reached a terminal status — cancelling twice is not an error
// the caller needs to retry, but it is also not silently a no-op: the
// caller gets an explicit answer instead of a run.cancelled event nobody
// asked for a second time.
var ErrRunAlreadyTerminal = fmt.Errorf("engine: run is already in a terminal state")

// ErrRunNotCancellable is returned for a status domain.legalRunEvents does
// not accept RunCancelled from (currently: Pending) — a run that has not
// yet started a node has nothing running to interrupt, and 03-workflows.md
// never gave Pending a cancel transition (see internal/domain/transitions.go).
var ErrRunNotCancellable = fmt.Errorf("engine: run cannot be cancelled from its current status")

// Cancel is `kairos cancel`'s (09-cli-and-tui.md) daemon-side entry point —
// the one piece L20-webui.md's own mutations.go left unbuilt ("the
// underlying cli.Client methods already exist" was true for fork/pause but
// NOT for cancel: no Engine.Cancel, no API route, no CLI verb existed
// anywhere in this tree before this pass — see L23-webui-revamp.md).
//
// The domain-level machinery this method drives has existed since
// domain.RunCancelled was added (advanceRunCancelled signals every
// non-terminal node execution; shard.go's processEvent already runs
// compensateRun automatically on the Running/Degraded -> Cancelled
// transition, reversing applied effects in reverse order, unconditionally
// — there is no "--compensate" toggle to thread through because the
// domain event carries none and shard.go's compensation is unconditional
// today, matching 05-gates.md's compensation contract as actually built,
// not 09-cli-and-tui.md's more qualified "--compensate to unwind" prose).
// What was missing was purely the append: an entry point that appends
// RunCancelled onto runID's own stream, exactly the shape AnswerHumanTask
// and GrantWaiver already use.
func (e *Engine) Cancel(ctx context.Context, runID, reason string) error {
	if reason == "" {
		return ErrCancelReasonRequired
	}

	state, ok, err := e.store.GetRunState(ctx, runID)
	if err != nil {
		return fmt.Errorf("loading run state for %s: %w", runID, err)
	}
	if !ok {
		return fmt.Errorf("engine: no such run %s", runID)
	}
	if state.Status.Terminal() {
		return fmt.Errorf("%w: %s is %s", ErrRunAlreadyTerminal, runID, state.Status)
	}
	if state.Status != domain.RunRunning && state.Status != domain.RunDegradedS {
		return fmt.Errorf("%w: %s is %s", ErrRunNotCancellable, runID, state.Status)
	}

	ev := domain.RunCancelled{RunID: runID, Reason: reason}
	// Reachable from a separate `kairos cancel` process racing the live
	// engine's own busy shard, exactly AnswerHumanTask's profile — the
	// higher (50) retry budget, not appendNext's plain 5.
	return e.appendNextHumanFacing(ctx, runID, ev)
}
