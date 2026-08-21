package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/williamokano/kairos/internal/admission"
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
		e.armTimer(c)
		return nil
	case domain.CmdCreateHumanTask:
		return e.dispatchCreateHumanTask(ctx, c)
	case domain.CmdSpawnChildren:
		return e.dispatchSpawnChildren(ctx, definitionRef, c, false)
	default:
		return fmt.Errorf("engine: unknown cmd type %T", cmd)
	}
}

// loadDefinition resolves the constitution (L11: kairos/baseline +
// e.constitutionProjectPath) on every call, merging its gate library into
// def.Gates and attaching MandatoryBaselineGateIDs to every node. The
// repo-level layer (05-gates.md's third tier, hash-pinned at run start)
// is resolved separately by evaluateGates once a workspace directory is
// known — see gates.go's mergeRepoConstitution.
func (e *Engine) loadDefinition(definitionRef string) (registry.Definition, error) {
	if definitionRef == "" {
		return registry.Definition{}, fmt.Errorf("engine: empty definitionRef")
	}
	return registry.LoadWithConstitution(definitionRef, "", e.constitutionProjectPath)
}

func (e *Engine) dispatchStartNode(ctx context.Context, definitionRef string, c domain.CmdStartNode) error {
	def, err := e.loadDefinition(definitionRef)
	if err != nil {
		return fmt.Errorf("loading definition: %w", err)
	}
	nd, actor, found := resolveNodeActor(def, c)
	if !found {
		return fmt.Errorf("node %q not found in definition %s", c.NodeID, definitionRef)
	}

	// L11: policy answers "may this actor cause this outward mutation?"
	// before admission even runs — a denied or unconfirmed effect means
	// there is nothing to admit. See checkEffects's doc comment for this
	// document's synchronous-check scope (not the full pause-and-resume
	// confirmation flow).
	if blocked, err := e.checkEffects(ctx, c, nd); err != nil {
		return err
	} else if blocked {
		return nil
	}

	// L07: admission answers "may it start right now?" before any actor
	// ever spawns a process — see internal/admission and
	// L07-admission.md. A Queued outcome never falls through to the actor
	// switch below: nothing has been recorded for this exec yet
	// (NodeExecutionStarted is only appended by the actor dispatch
	// functions themselves), so it is retried later with no side effect
	// to unwind. A Denied outcome DOES record NodeExecutionStarted first
	// — domain's legalExecEvents table only accepts NodeExecutionFailed
	// against an Executing exec (ExecPending accepts only Started), so a
	// denial is represented as a zero-duration started-then-failed
	// attempt rather than an illegal Pending->Failed transition.
	req := e.admissionRequest(nd, actor)
	decision := e.admitOrQueue(definitionRef, c, req)
	switch decision.Outcome {
	case admission.Denied:
		return e.denyNode(ctx, c, decision.Reason)
	case admission.Queued:
		return nil
	}

	e.storeClaim(c.ExecID, decision.Claims)
	return e.runActorDispatch(ctx, nd, c, actor)
}

// runActorDispatch is the actor switch itself, factored out of
// dispatchStartNode so drainPending (admission.go) can re-run it for a
// previously Queued node execution without duplicating the switch. Every
// actor here is responsible for releasing c.ExecID's admission claim
// (via e.releaseAndDrain) once its work is truly finished — synchronous
// actors (rule) release before returning; actors that spawn a background
// reaper (shell, llm) transfer that responsibility to the reaper.
func (e *Engine) runActorDispatch(ctx context.Context, nd registry.NodeDef, c domain.CmdStartNode, actor string) error {
	switch actor {
	case "rule":
		defer e.releaseAndDrain(ctx, c.ExecID)
		return e.dispatchRuleActor(ctx, c)
	case "shell":
		return e.dispatchShellActor(ctx, nd, c)
	case "claude", "codex", "gemini", "local":
		return e.dispatchLLMActor(ctx, nd, c, actor)
	case "effect":
		return e.dispatchEffectActor(ctx, nd, c)
	default:
		// human/builtin.* actors are out of L08's scope too — the engine
		// fails the node honestly rather than hanging forever.
		defer e.releaseAndDrain(ctx, c.ExecID)
		return e.startThenFail(ctx, c, domain.FailFailure,
			fmt.Sprintf("actor %q is not implemented", actor))
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
	// "A wait's entire footprint is three rows" (06-durability.md) — L05
	// through L11 had no wait-bearing node in scope, so this only logged.
	// L14 makes WaitConversation real, but still without a dedicated
	// waiters/timers SQL table: the NodeExecution's own ExecWaiting row
	// (already committed by the time this runs — see advance.go's
	// dispatchExec) plus the Conversation's own event stream ARE the
	// rows; resolveConversationWait (conversation.go) re-derives
	// resolution from them on every new message and on every Reconcile,
	// no separate bookkeeping needed. Every other WaitKind (poll,
	// webhook, human, timer) remains a documented gap — Future work, not
	// silently dropped.
	e.log.Info("node entered wait", "runID", c.RunID, "nodeID", c.NodeID, "kind", c.Wait.Kind)
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

// startThenFail records c as a zero-duration started-then-failed attempt:
// domain's legalExecEvents table only accepts NodeExecutionFailed against
// an Executing exec (ExecPending accepts only Started), so any failure
// discovered before a process is actually spawned — an admission denial,
// a policy denial, a missing workspace config, a preflight binary that
// doesn't exist, Start() itself returning an error — has no legal
// Pending->Failed transition without first recording that the attempt
// began. This is the one shared mechanism every such call site must
// route through (NL-31): using appendNodeFailed directly here is exactly
// the illegal-transition trap this function exists to close.
func (e *Engine) startThenFail(ctx context.Context, c domain.CmdStartNode, reason domain.FailReason, message string) error {
	if err := e.appendNext(ctx, c.RunID, domain.NodeExecutionStarted(c)); err != nil {
		return err
	}
	return e.appendNodeFailed(ctx, c.RunID, c.NodeID, c.ExecID, reason, message)
}

// denyNode is startThenFail for an admission denial specifically — the
// "denied: " prefix names the outcome as a refusal to run at all, not a
// failure encountered while running.
func (e *Engine) denyNode(ctx context.Context, c domain.CmdStartNode, reason string) error {
	return e.startThenFail(ctx, c, domain.FailFailure, "denied: "+reason)
}

// denyNodeWithReason is denyNode with a caller-chosen FailReason — L11's
// policy checks (internal/engine/policy.go) use domain.FailPolicyDenied
// so a policy denial is distinguishable from an admission denial or an
// ordinary actor failure, both of which stay FailFailure.
func (e *Engine) denyNodeWithReason(ctx context.Context, c domain.CmdStartNode, reason domain.FailReason, message string) error {
	return e.startThenFail(ctx, c, reason, "denied: "+message)
}

func (e *Engine) appendInterrupted(ctx context.Context, runID, nodeID, execID string) error {
	return e.appendNext(ctx, runID, domain.NodeExecutionInterrupted{RunID: runID, NodeID: nodeID, ExecID: execID})
}
