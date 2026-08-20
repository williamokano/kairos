package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/eventstore"
	"github.com/williamokano/kairos/internal/executor/local"
	"github.com/williamokano/kairos/internal/registry"
)

// dispatch turns one domain.Cmd into an action. definitionRef resolves the
// rich registry.Definition a CmdStartNode/CmdEvaluateGates needs — Cmds
// themselves carry only routing-relevant fields (RunID/NodeID/ExecID),
// exactly like domain.Graph does not carry actor/gates (L03's
// ProjectGraph doc comment).
func (e *Engine) dispatch(ctx context.Context, definitionRef string, cmd domain.Cmd) error {
	switch c := cmd.(type) {
	case domain.CmdStartNode:
		return e.dispatchStartNode(ctx, definitionRef, c)
	case domain.CmdEvaluateGates:
		return e.dispatchEvaluateGates(ctx, definitionRef, c)
	case domain.CmdSignalNode:
		return e.dispatchSignalNode(ctx, c)
	case domain.CmdEnterWait:
		return e.dispatchEnterWait(ctx, c)
	case domain.CmdArmTimer:
		// No persisted timer wheel in L05 (documented decision #6) — the
		// wait row CmdEnterWait recorded is the only bookkeeping; a real
		// wall-clock-armed timer is Future work. Not silently dropped:
		// logged so an actual wait-bearing workflow's gap is visible.
		e.log.Warn("CmdArmTimer has no real timer wheel yet (L05 scope)", "runID", c.RunID, "nodeID", c.NodeID, "fireAt", c.FireAt)
		return nil
	case domain.CmdCreateHumanTask:
		e.log.Warn("CmdCreateHumanTask has no human queue yet (L13 scope)", "runID", c.RunID, "nodeID", c.NodeID)
		return nil
	default:
		return fmt.Errorf("engine: unknown cmd type %T", cmd)
	}
}

func (e *Engine) loadDefinition(definitionRef string) (registry.Definition, error) {
	if definitionRef == "" {
		return registry.Definition{}, fmt.Errorf("engine: empty definitionRef")
	}
	return registry.Load(definitionRef)
}

func (e *Engine) dispatchStartNode(ctx context.Context, definitionRef string, c domain.CmdStartNode) error {
	def, err := e.loadDefinition(definitionRef)
	if err != nil {
		return fmt.Errorf("loading definition: %w", err)
	}
	var nd registry.NodeDef
	found := false
	for _, n := range def.Nodes {
		if string(n.ID) == c.NodeID {
			nd = n
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("node %q not found in definition %s", c.NodeID, definitionRef)
	}

	switch nd.Actor {
	case "rule":
		return e.dispatchRuleActor(ctx, c)
	case "shell":
		return e.dispatchShellActor(ctx, nd, c)
	default:
		// llm/claude/codex/gemini/human/builtin.* actors are out of L05's
		// scope (L08's actor SDK) — the milestone workflow never names
		// one, but a future workflow might; fail the node honestly
		// rather than hanging forever.
		return e.appendNodeFailed(ctx, c.RunID, c.NodeID, c.ExecID, domain.FailFailure,
			fmt.Sprintf("actor %q is not implemented (requires L08's actor SDK)", nd.Actor))
	}
}

func (e *Engine) dispatchEvaluateGates(ctx context.Context, definitionRef string, c domain.CmdEvaluateGates) error {
	def, err := e.loadDefinition(definitionRef)
	if err != nil {
		return fmt.Errorf("loading definition: %w", err)
	}
	return e.evaluateGates(ctx, def, c)
}

func (e *Engine) dispatchSignalNode(ctx context.Context, c domain.CmdSignalNode) error {
	dir := e.scratchDir(c.RunID, c.ExecID)
	rec, ok, err := local.ReadProcRecord(dir)
	if err != nil {
		return fmt.Errorf("reading proc record for signal: %w", err)
	}
	if !ok {
		// Nothing was ever spawned for this exec (e.g. it never left
		// Pending) — nothing to signal.
		return nil
	}
	// NodeExecutionInterrupted is recorded BEFORE killing, per the L05
	// milestone: a restart must never have to guess whether a node was
	// interrupted (12-build-plan.md).
	if err := e.appendInterrupted(ctx, c.RunID, c.NodeID, c.ExecID); err != nil {
		return fmt.Errorf("recording interruption before signalling: %w", err)
	}
	// Cancel runs the TERM->grace->KILL sequence (01-architecture.md) via
	// the Executor interface — real for local.Local, recorded for
	// exectest.Fake in unit tests.
	return e.exec.Cancel(ctx, rec.PGID, e.killGrace)
}

func (e *Engine) dispatchEnterWait(ctx context.Context, c domain.CmdEnterWait) error {
	// "A wait's entire footprint is three rows" (06-durability.md) — for
	// L05 there is no wait-bookkeeping table (no wait-bearing node exists
	// in the milestone workflow), so this only logs; a real
	// waiters/timers implementation is Future work.
	e.log.Info("node entered wait (no real wait bookkeeping table yet)", "runID", c.RunID, "nodeID", c.NodeID, "kind", c.Wait.Kind)
	return nil
}

// appendNext reads the stream's current length as the CAS expectedSeq and
// appends ev, retrying on ErrConflict a bounded number of times — needed
// because dispatch actions (a reaper goroutine reporting a process exit,
// say) run outside the shard's own single-goroutine ordering guarantee.
func (e *Engine) appendNext(ctx context.Context, runID string, ev domain.Event) error {
	const maxRetries = 5
	for i := 0; i < maxRetries; i++ {
		envs, err := e.store.Read(ctx, runID)
		if err != nil {
			return fmt.Errorf("reading stream %s: %w", runID, err)
		}
		_, err = e.store.AppendIf(ctx, runID, len(envs), []domain.Event{ev}, eventstore.AppendMeta{
			Actor:         "engine",
			CorrelationID: runID,
			OccurredAt:    time.Now().UTC(),
		})
		if err == nil {
			return nil
		}
		if err == eventstore.ErrConflict {
			continue
		}
		return fmt.Errorf("appending %s: %w", ev.EventType(), err)
	}
	return fmt.Errorf("appending %s: exhausted retries on conflict", ev.EventType())
}

func (e *Engine) appendNodeFailed(ctx context.Context, runID, nodeID, execID string, reason domain.FailReason, message string) error {
	return e.appendNext(ctx, runID, domain.NodeExecutionFailed{
		RunID: runID, NodeID: nodeID, ExecID: execID, Reason: reason, Message: message,
	})
}

func (e *Engine) appendInterrupted(ctx context.Context, runID, nodeID, execID string) error {
	return e.appendNext(ctx, runID, domain.NodeExecutionInterrupted{RunID: runID, NodeID: nodeID, ExecID: execID})
}
