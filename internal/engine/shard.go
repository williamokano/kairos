package engine

import (
	"context"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/events"
	"github.com/williamokano/kairos/internal/executor/local"
)

// shard owns a subset of runs (by hash(runID)) and processes their events
// one at a time, in order, on one goroutine — so a run's fold and dispatch
// can never race with itself, matching 01-architecture.md's
// runShards[N] design.
type shard struct {
	engine *Engine
	queue  chan events.Envelope
	states map[string]domain.RunState // runID -> this shard's own fold; only this goroutine touches it after Start

	// defRefs tracks each run's DefinitionRef, captured off TriggerReceived
	// — domain.RunState has no field for it (Graph is routing-only,
	// per L03's ProjectGraph), but dispatch needs it to load the rich
	// registry.Definition for actor/gates/restart-policy lookups.
	defRefs map[string]string
}

func newShard(e *Engine) *shard {
	return &shard{
		engine:  e,
		queue:   make(chan events.Envelope, 256),
		states:  make(map[string]domain.RunState),
		defRefs: make(map[string]string),
	}
}

// prime seeds state for runID before the shard's run loop starts — see
// Engine.Start's doc comment on why this must happen before subscribing.
func (s *shard) prime(runID string, state domain.RunState, definitionRef string) {
	s.states[runID] = state
	s.defRefs[runID] = definitionRef
}

// enqueue routes env to this shard's queue. Called from Engine's single
// router goroutine, never concurrently with itself.
func (s *shard) enqueue(env events.Envelope) {
	s.queue <- env
}

// run processes this shard's queue until ctx is cancelled. On
// cancellation it interrupts every node it currently owns that is
// ExecStatus==Executing BEFORE returning — "the daemon records
// node.execution.interrupted before it kills" (12-build-plan.md's
// SIGINT/SIGTERM handling) — safe to do here, race-free, because only
// this goroutine ever touches s.states.
func (s *shard) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			s.interruptExecuting()
			return
		case env := <-s.queue:
			s.process(ctx, env)
		}
	}
}

// interruptExecuting records NodeExecutionInterrupted for, then cancels,
// every currently-Executing node this shard owns. Uses a background
// context deliberately: the shard's own ctx is already cancelled by the
// time this runs, but recording the interruption and killing the process
// group must still complete.
func (s *shard) interruptExecuting() {
	ctx := context.Background()
	for runID, state := range s.states {
		for nodeID, execs := range state.Executions {
			if len(execs) == 0 {
				continue
			}
			exec := execs[len(execs)-1]
			if exec.Status != domain.ExecExecuting {
				continue
			}
			dir := s.engine.scratchDir(runID, exec.ExecID)
			rec, ok, err := local.ReadProcRecord(dir)
			if err != nil || !ok {
				continue
			}
			if err := s.engine.appendInterrupted(ctx, runID, string(nodeID), exec.ExecID); err != nil {
				s.engine.log.Error("recording interruption on shutdown failed", "runID", runID, "nodeID", nodeID, "err", err)
				continue
			}
			if err := s.engine.exec.Cancel(ctx, rec.PGID, s.engine.killGrace); err != nil {
				s.engine.log.Error("cancelling process group on shutdown failed", "runID", runID, "nodeID", nodeID, "err", err)
			}
		}
	}
}

func (s *shard) process(ctx context.Context, env events.Envelope) {
	if trigger, ok := env.Event.(domain.TriggerReceived); ok {
		s.defRefs[env.StreamID] = trigger.DefinitionRef
	}

	before := s.states[env.StreamID] // zero-value domain.RunState{} for a brand-new run's first event
	after, cmds, err := domain.Advance(before, env.Event, env.OccurredAt)
	if err != nil {
		s.engine.log.Error("advance failed", "runID", env.StreamID, "eventType", env.EventType, "err", err)
		return
	}
	s.states[env.StreamID] = after

	defRef := s.defRefs[env.StreamID]
	for _, cmd := range cmds {
		if err := s.engine.dispatch(ctx, defRef, cmd); err != nil {
			s.engine.log.Error("dispatch failed", "runID", env.StreamID, "err", err)
		}
	}

	// L12: a run that just newly reached Cancelled or Failed may have
	// already-applied effects (git.push, gh.pr.create) needing reverse-
	// order compensation — 05-gates.md's compensation contract. Checked
	// by comparing before/after so this fires exactly once, on the
	// transition, not on every subsequent event this now-terminal run's
	// stream will never actually receive. Runs in a goroutine: a real
	// `gh pr close` call must not block this shard's processing of every
	// other run it owns.
	if !before.Status.Terminal() && after.Status.Terminal() &&
		(after.Status == domain.RunCancelledS || after.Status == domain.RunFailed) {
		s.engine.wg.Add(1)
		go func(runID string) {
			defer s.engine.wg.Done()
			s.engine.compensateRun(context.Background(), runID)
		}(env.StreamID)
	}

	// L17: this run may be a spawned child — on ANY terminal transition
	// (success included), give its coordinator's join a chance to
	// progress or resolve. Cheap no-op for the overwhelming majority of
	// runs, which were never spawned (handleChildRunFinished's first
	// step is reading this run's own TriggerRef and returning immediately
	// on a mismatched prefix).
	if !before.Status.Terminal() && after.Status.Terminal() {
		runID := env.StreamID
		status := after.Status
		s.engine.wg.Add(1)
		go func() {
			defer s.engine.wg.Done()
			s.engine.handleChildRunFinished(context.Background(), runID, status)
		}()
	}
}
