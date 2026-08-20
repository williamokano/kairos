package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/effect"
	"github.com/williamokano/kairos/internal/registry"
)

// Approve is `kairos approve`'s single entry point for every kind of
// parked confirmation — a wait: human node's decision AND an
// actor: effect node's confirm-tier park, kept as one CLI/API verb
// rather than growing a second, effect-specific one (reusing L13's
// anti-rubber-stamp discipline: no --yes/--all here either, same as
// AnswerHumanTask). Tries AnswerHumanTask first; ErrNotAWaitHumanNode is
// the signal this is actually a parked effect confirmation instead.
// decision "approve" maps to Approved: true, anything else to false —
// matching AnswerHumanTask's own free-form Decision string, since
// 05-gates.md's own CLI example is `kairos approve run_01J8 --effect pr
// --yes` and this document's CLI keeps the existing --confirm flag shape
// (internal/cli/approve.go) rather than adding a distinct --effect flag.
func (e *Engine) Approve(ctx context.Context, runID, nodeID string, ans AnswerDecision) error {
	err := e.AnswerHumanTask(ctx, runID, nodeID, ans)
	if err == nil || !errors.Is(err, ErrNotAWaitHumanNode) {
		return err
	}
	return e.AnswerEffectConfirmation(ctx, runID, nodeID, ans.Decision == "approve", ans.Reason)
}

// isLive reports whether Start has been called — the same distinction
// armTimer's runCtx nil-check already makes, reused here because a
// CmdStartNode dispatched from Reconcile's own recoverLost path (before
// the live Subscribe loop exists) cannot rely on that loop to fold and
// dispatch an event this function appends; it must do so itself.
func (e *Engine) isLive() bool {
	e.runCtxMu.Lock()
	defer e.runCtxMu.Unlock()
	return e.runCtx != nil
}

// parkForEffectConfirmation appends EffectConfirmationParked — see that
// event's doc comment for the real Pending->Waiting transition it causes
// and why "RELEASE ALL PERMITS" is trivial here (checkEffects runs
// before admission ever grants a claim). While the engine is live, the
// event lands on runID's own stream, which the live Subscribe loop is
// already watching — the owning shard folds it and dispatches the
// resulting CmdCreateHumanTask, exactly like AnswerHumanTask's own
// append-only discipline (human.go's doc comment on the double-dispatch
// bug that pattern avoids). Before Start, no such loop exists, so this
// folds and dispatches synchronously itself instead.
func (e *Engine) parkForEffectConfirmation(ctx context.Context, c domain.CmdStartNode, effectName string) error {
	ev := domain.EffectConfirmationParked{RunID: c.RunID, NodeID: c.NodeID, ExecID: c.ExecID, Effect: effectName}
	if e.isLive() {
		return e.appendNext(ctx, c.RunID, ev)
	}
	// Reconcile-time: appendAndFoldBeforeStart captures state BEFORE
	// appending — AppendIf already folds ev transactionally into the
	// store's own projection as part of recording it, so state fetched
	// AFTER would already reflect ev; re-running domain.Advance against
	// that post-fold state would apply ev a second time.
	return e.appendAndFoldBeforeStart(ctx, c.RunID, ev)
}

// foldAndDispatch re-derives cmds for ev against preState — the state
// captured BEFORE ev was appended, never reloaded from the store after
// (see parkForEffectConfirmation's doc comment on the double-fold bug
// that would otherwise cause) — and dispatches them. The Reconcile-time
// counterpart to the live shard's own process() loop, used only before
// Start exists (see isLive's doc comment; recoverLost already
// establishes this same direct-dispatch pattern for retry Cmds).
func (e *Engine) foldAndDispatch(ctx context.Context, runID string, preState domain.RunState, ev domain.Event) error {
	_, cmds, err := domain.Advance(preState, ev, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("folding %s: %w", ev.EventType(), err)
	}
	definitionRef, err := e.firstEventDefinitionRef(ctx, runID)
	if err != nil {
		return fmt.Errorf("resolving definitionRef for %s: %w", runID, err)
	}
	for _, cmd := range cmds {
		if err := e.dispatch(ctx, definitionRef, cmd); err != nil {
			return fmt.Errorf("dispatching cmd for %s: %w", ev.EventType(), err)
		}
	}
	return nil
}

// AnswerEffectConfirmation is the one and only path
// EffectConfirmationAnswered is ever appended from — kairos approve's
// effect-confirmation counterpart to L13's AnswerHumanTask, kept
// separate from it because a parked effect-confirmation node has no
// nd.Wait declaration for AnswerHumanTask's waitsOnHuman check to find
// (the node IS the effect, not a wait: human node).
func (e *Engine) AnswerEffectConfirmation(ctx context.Context, runID, nodeID string, approved bool, reason string) error {
	if reason == "" {
		return ErrHumanDecisionReasonRequired
	}
	state, ok, err := e.store.GetRunState(ctx, runID)
	if err != nil {
		return fmt.Errorf("loading run state for %s: %w", runID, err)
	}
	if !ok {
		return fmt.Errorf("engine: no such run %s", runID)
	}
	execs, ok := state.Executions[domain.NodeID(nodeID)]
	if !ok || len(execs) == 0 {
		return fmt.Errorf("engine: node %s has no execution in run %s", nodeID, runID)
	}
	exec := execs[len(execs)-1]
	if exec.Status != domain.ExecWaiting {
		return fmt.Errorf("engine: node %s is not currently parked on an effect confirmation (status %s)", nodeID, exec.Status)
	}

	if approved {
		// The confirmation itself, recorded before the resume so
		// checkEffects's re-run of dispatchStartNode finds it — same
		// audit fact L11 already defined.
		var effectName string
		if nd, ok := e.resolveNode(e.mustDefinitionRef(ctx, runID), nodeID); ok && len(nd.Effects) > 0 {
			effectName = nd.Effects[0]
		}
		if err := e.appendNext(ctx, runID, domain.EffectConfirmed{RunID: runID, NodeID: nodeID, Effect: effectName, Scope: "once"}); err != nil {
			return err
		}
	}

	ev := domain.EffectConfirmationAnswered{RunID: runID, NodeID: nodeID, ExecID: exec.ExecID, Approved: approved, Reason: reason}
	// Append only — the live shard picks this up, exactly like
	// AnswerHumanTask (see that function's doc comment on the
	// double-dispatch bug this avoids).
	return e.appendNext(ctx, runID, ev)
}

// mustDefinitionRef is firstEventDefinitionRef with errors swallowed to
// empty string — used only where a missing definitionRef degrades to
// "no effect name recorded" rather than blocking the confirmation
// answer itself.
func (e *Engine) mustDefinitionRef(ctx context.Context, runID string) string {
	ref, err := e.firstEventDefinitionRef(ctx, runID)
	if err != nil {
		return ""
	}
	return ref
}

// dispatchEffectActor makes actor: effect real: resolve the node's one
// declared builtin, check the unattended ceiling, honour DryRun, then
// run the full attempted->applied|failed state machine through the
// resolved effect.Provider. Synchronous, like the rule actor — a git
// push or a `gh pr create` is a bounded external call, not a long-running
// process needing a background reaper.
func (e *Engine) dispatchEffectActor(ctx context.Context, nd registry.NodeDef, c domain.CmdStartNode) error {
	defer e.releaseAndDrain(ctx, c.ExecID)

	effectName := nd.Effects[0]
	lineageRoot := e.lineageRootFor(ctx, c.RunID)
	idempotencyKey := effect.IdempotencyKey(lineageRoot, c.NodeID, effectName)

	if err := e.appendNext(ctx, c.RunID, domain.NodeExecutionStarted(c)); err != nil {
		return err
	}

	if e.dryRun {
		if err := e.appendNext(ctx, c.RunID, domain.EffectSimulated{RunID: c.RunID, NodeID: c.NodeID, ExecID: c.ExecID, Effect: effectName}); err != nil {
			return err
		}
		return e.appendEffectOutput(ctx, c, map[string]any{"simulated": true})
	}

	if ceiling, ok := e.effectCeilings[effectName]; ok {
		count, err := e.countEffectApplied(ctx, c.RunID, effectName)
		if err != nil {
			return err
		}
		if count >= ceiling {
			return e.failEffectNode(ctx, c.RunID, c.NodeID, c.ExecID, domain.FailPolicyDenied,
				fmt.Sprintf("effect %q: unattended ceiling reached (%d applied this run, cap %d)", effectName, count, ceiling))
		}
	}

	provider, ok := e.effectProviders[effectName]
	if !ok {
		return e.failEffectNode(ctx, c.RunID, c.NodeID, c.ExecID, domain.FailFailure,
			fmt.Sprintf("no builtin provider registered for effect %q", effectName))
	}

	workDir, err := e.workspaceRepoDirFor(ctx, c.RunID)
	if err != nil {
		return e.failEffectNode(ctx, c.RunID, c.NodeID, c.ExecID, domain.FailFailure, err.Error())
	}

	// effect.attempted precedes the call — "decision before action"
	// (06-durability.md): a crash mid-call leaves this as the last fact,
	// and reconciliation probes by IdempotencyKey instead of blindly
	// retrying.
	if err := e.appendNext(ctx, c.RunID, domain.EffectAttempted{
		RunID: c.RunID, NodeID: c.NodeID, ExecID: c.ExecID, Effect: effectName, IdempotencyKey: idempotencyKey,
	}); err != nil {
		return err
	}

	req := effect.Request{
		RunID: c.RunID, NodeID: c.NodeID, ExecID: c.ExecID,
		Effect: effectName, IdempotencyKey: idempotencyKey,
		WorkDir: workDir, Dir: e.scratchDir(c.RunID, c.ExecID),
		Args: nd.With,
	}
	res, err := provider.Attempt(ctx, req)
	if err != nil {
		if failErr := e.appendNext(ctx, c.RunID, domain.EffectFailed{RunID: c.RunID, NodeID: c.NodeID, ExecID: c.ExecID, Effect: effectName, Reason: err.Error()}); failErr != nil {
			return failErr
		}
		return e.failEffectNode(ctx, c.RunID, c.NodeID, c.ExecID, domain.FailFailure, err.Error())
	}
	if res.Outcome == effect.Failed {
		if err := e.appendNext(ctx, c.RunID, domain.EffectFailed{RunID: c.RunID, NodeID: c.NodeID, ExecID: c.ExecID, Effect: effectName, Reason: res.Reason}); err != nil {
			return err
		}
		return e.failEffectNode(ctx, c.RunID, c.NodeID, c.ExecID, domain.FailFailure, res.Reason)
	}

	if err := e.appendNext(ctx, c.RunID, domain.EffectApplied{RunID: c.RunID, NodeID: c.NodeID, ExecID: c.ExecID, Effect: effectName, ExternalRef: res.ExternalRef}); err != nil {
		return err
	}
	return e.appendEffectOutput(ctx, c, map[string]any{"externalRef": res.ExternalRef})
}

// appendEffectOutput folds body as the node's typed output — always
// schema-valid, since requiresOutputSchema exempts actor: effect
// (registry/defaults.go) exactly like the rule actor.
func (e *Engine) appendEffectOutput(ctx context.Context, c domain.CmdStartNode, body map[string]any) error {
	out, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshalling effect output: %w", err)
	}
	ev := domain.NodeOutputReceived{RunID: c.RunID, NodeID: c.NodeID, ExecID: c.ExecID, SchemaValid: true, Output: json.RawMessage(out)}
	return e.finishEffectNode(ctx, c.RunID, ev)
}

// failEffectNode is appendNodeFailed's dispatchEffectActor-local
// counterpart — see finishEffectNode's doc comment for why this document
// cannot just call the shared appendNodeFailed helper directly.
func (e *Engine) failEffectNode(ctx context.Context, runID, nodeID, execID string, reason domain.FailReason, message string) error {
	ev := domain.NodeExecutionFailed{RunID: runID, NodeID: nodeID, ExecID: execID, Reason: reason, Message: message}
	return e.finishEffectNode(ctx, runID, ev)
}

// finishEffectNode appends ev (NodeOutputReceived or NodeExecutionFailed
// — dispatchEffectActor's two possible terminal events) and, unless the
// live shard loop is already watching this run's stream, folds and
// dispatches it itself. dispatchEffectActor is synchronous (no
// background reaper, unlike dispatchShellActor/dispatchLLMActor), so a
// retry Reconcile's own recoverLost dispatches during startup completes
// entirely inside Reconcile — before Start's live Subscribe loop exists
// — and would otherwise leave the resulting CmdEvaluateGates/routing
// Cmds permanently undispatched, the same double-fold-shaped gap
// parkForEffectConfirmation's own doc comment describes (there, the fix
// is capturing state before the append; here, the fix is capturing it
// before the append too, via appendAndFoldBeforeStart).
func (e *Engine) finishEffectNode(ctx context.Context, runID string, ev domain.Event) error {
	if e.isLive() {
		return e.appendNext(ctx, runID, ev)
	}
	return e.appendAndFoldBeforeStart(ctx, runID, ev)
}

// workspaceRepoDirFor is the effect providers' WorkDir — the daemon-wide
// clone WorkspaceRepo provisions, same single-repo scope and same
// provisioning call as dispatchShellActor's own workspace: write
// handling (L06/L12's Documented decisions). A missing WorkspaceRepo or
// a provisioning failure is an honest error, never a silent fallback to
// the daemon's own cwd (AGENTS §4 rule 1).
func (e *Engine) workspaceRepoDirFor(ctx context.Context, runID string) (string, error) {
	if e.workspaceRepo == "" {
		return "", fmt.Errorf("actor \"effect\" requires a configured WorkspaceRepo")
	}
	ws, err := e.workspaces.Provision(ctx, runID, e.workspaceRepo)
	if err != nil {
		return "", fmt.Errorf("provisioning workspace: %w", err)
	}
	return ws.Dir, nil
}

// countEffectApplied counts EffectApplied facts for (runID, effect) —
// the unattended-ceiling check's evidence, and also what a fresh
// idempotency probe (reconcile.go) consults to avoid re-attempting an
// effect the log already shows succeeded.
func (e *Engine) countEffectApplied(ctx context.Context, runID, effectName string) (int, error) {
	envs, err := e.store.Read(ctx, runID)
	if err != nil {
		return 0, fmt.Errorf("reading stream %s: %w", runID, err)
	}
	n := 0
	for _, env := range envs {
		if ev, ok := env.Event.(domain.EffectApplied); ok && ev.Effect == effectName {
			n++
		}
	}
	return n, nil
}
