package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/effect"
	"github.com/williamokano/kairos/internal/executor/local"
	"github.com/williamokano/kairos/internal/registry"
)

// readOutputIfAny reads dir/output.json and reports whether it exists and
// is valid JSON — used when a node's process is confirmed dead but may
// have written its result before dying.
func readOutputIfAny(dir string) (json.RawMessage, bool) {
	body, err := os.ReadFile(filepath.Join(dir, "output.json"))
	if err != nil || !json.Valid(body) {
		return nil, false
	}
	return json.RawMessage(body), true
}

// ReconcileReport summarises what startup reconciliation found and did —
// the payload of the EngineReconciled readiness-gate event.
type ReconcileReport struct {
	Recovered     int
	Lost          int
	OrphansReaped int
}

// Reconcile implements 06-durability.md's recovery sequence steps 5-11:
// probe every non-terminal run's Executing NodeExecutions against
// reality, resolve verdicts (alive/dead/unverifiable), re-dispatch owed
// actions, and append EngineReconciled — the event that gates the daemon
// serving its API (09-cli-and-tui.md: "readiness flips only after
// engine.reconciled appears"). Must be called, and must complete, before
// Start.
func (e *Engine) Reconcile(ctx context.Context) (ReconcileReport, error) {
	bootID, err := e.bootID.Current()
	if err != nil {
		return ReconcileReport{}, fmt.Errorf("reading boot id: %w", err)
	}
	if _, err := e.appendSystem(ctx, domain.EngineStarted{BootID: bootID}); err != nil {
		return ReconcileReport{}, fmt.Errorf("recording engine.started: %w", err)
	}

	runs, err := e.store.ListRuns(ctx, nil)
	if err != nil {
		return ReconcileReport{}, fmt.Errorf("listing runs: %w", err)
	}

	var report ReconcileReport
	for _, r := range runs {
		if r.Status.Terminal() {
			continue
		}
		n, err := e.reconcileRun(ctx, r.RunID, bootID)
		if err != nil {
			return ReconcileReport{}, fmt.Errorf("reconciling run %s: %w", r.RunID, err)
		}
		report.Recovered += n.recovered
		report.Lost += n.lost
		report.OrphansReaped += n.orphansReaped
	}

	if _, err := e.appendSystem(ctx, domain.EngineReconciled{
		Recovered: report.Recovered, Lost: report.Lost, OrphansReaped: report.OrphansReaped,
	}); err != nil {
		return ReconcileReport{}, fmt.Errorf("recording engine.reconciled: %w", err)
	}
	return report, nil
}

type reconcileCounts struct {
	recovered, lost, orphansReaped int
}

// reconcileRun probes every ExecStatus==Executing NodeExecution in one
// run's folded state and resolves it. Re-dispatch (a retry attempt, via
// domain.Advance's own NodeExecutionLost handling) is applied
// synchronously here, mirroring what a shard's process() does for a live
// event — Reconcile runs before any shard goroutine exists, so nothing
// else will ever pick these up.
func (e *Engine) reconcileRun(ctx context.Context, runID, bootID string) (reconcileCounts, error) {
	var counts reconcileCounts

	state, ok, err := e.store.GetRunState(ctx, runID)
	if err != nil {
		return counts, err
	}
	if !ok {
		return counts, nil
	}
	definitionRef, err := e.firstEventDefinitionRef(ctx, runID)
	if err != nil {
		return counts, err
	}

	for nodeID, execs := range state.Executions {
		if len(execs) == 0 {
			continue
		}
		exec := execs[len(execs)-1]
		if exec.Status == domain.ExecWaiting {
			// L17: a coordinator's join may have missed a child's
			// terminal transition entirely if the daemon was down when
			// it happened — handleChildRunFinished only fires for a
			// LIVE engine watching that child's shard. Catch up here,
			// the same way reconcileEffectNode catches up a missed
			// effect probe.
			if nd, ok := e.resolveNode(definitionRef, string(nodeID)); ok && nd.Wait != nil && len(nd.Wait.On) > 0 && nd.Wait.On[0].Kind == registry.WaitChildRun {
				if err := e.reconcileSpawnJoin(ctx, runID, string(nodeID), exec.ExecID, true); err != nil {
					return counts, fmt.Errorf("reconciling spawn join %s/%s: %w", nodeID, exec.ExecID, err)
				}
			}
			continue
		}
		if exec.Status != domain.ExecExecuting {
			continue
		}

		if nd, ok := e.resolveNode(definitionRef, string(nodeID)); ok && nd.Actor == "effect" {
			n, err := e.reconcileEffectNode(ctx, runID, string(nodeID), exec)
			if err != nil {
				return counts, fmt.Errorf("reconciling effect node %s/%s: %w", nodeID, exec.ExecID, err)
			}
			counts.recovered += n.recovered
			counts.lost += n.lost
			continue
		}

		dir := e.scratchDir(runID, exec.ExecID)
		rec, hasRec, err := local.ReadProcRecord(dir)
		if err != nil {
			return counts, fmt.Errorf("reading proc record for %s/%s: %w", nodeID, exec.ExecID, err)
		}

		verdict := local.VerdictDead
		if hasRec {
			verdict = local.Probe(rec, bootID)
		}

		switch verdict {
		case local.VerdictAlive:
			// Alive but not owned by any live engine (we just booted) —
			// an orphan by construction, even though its NodeExecution
			// row is not terminal. If the node's resolved RestartPolicy
			// is adopt, leave it running and watch for its real exit
			// (decision below) instead of killing it.
			if nd, ok := e.resolveNode(definitionRef, string(nodeID)); ok && nd.RestartPolicy == registry.RestartAdopt {
				e.wg.Add(1)
				go func(nd registry.NodeDef, runID, nodeID, execID string, rec local.ProcRecord) {
					defer e.wg.Done()
					e.adoptWatch(ctx, nd, runID, nodeID, execID, rec, bootID)
				}(nd, runID, string(nodeID), exec.ExecID, rec)
				counts.recovered++
				continue
			}
			if err := e.exec.Signal(ctx, rec.PGID, local.SignalKill); err != nil {
				e.log.Warn("failed to signal orphaned process group", "pgid", rec.PGID, "err", err)
			}
			if _, err := e.appendSystem(ctx, domain.ProcessOrphanReaped{PGID: rec.PGID}); err != nil {
				return counts, fmt.Errorf("recording process.orphan.reaped: %w", err)
			}
			counts.orphansReaped++
			if err := e.recoverLost(ctx, definitionRef, state, runID, string(nodeID), exec); err != nil {
				return counts, err
			}
			counts.lost++

		case local.VerdictDead:
			outcome, outcomeOK := readOutputIfAny(dir)
			if outcomeOK {
				if err := e.appendNext(ctx, runID, domain.NodeOutputReceived{
					RunID: runID, NodeID: string(nodeID), ExecID: exec.ExecID,
					SchemaValid: true, Output: outcome,
				}); err != nil {
					return counts, err
				}
				counts.recovered++
				continue
			}
			if err := e.recoverLost(ctx, definitionRef, state, runID, string(nodeID), exec); err != nil {
				return counts, err
			}
			counts.lost++

		case local.VerdictUnverifiable:
			// A real reboot: the recorded pgid may belong to a
			// stranger's process tree. Never signal it — only record
			// the fact and let retry-or-route decide what's next.
			if err := e.recoverLost(ctx, definitionRef, state, runID, string(nodeID), exec); err != nil {
				return counts, err
			}
			counts.lost++
		}
	}

	// wait: conversation catch-up (L14): a message may have arrived on
	// this run's Conversation stream while the daemon was down —
	// Store.Subscribe only delivers events appended after Start
	// subscribes, so the live loop alone would never see it. Cheap and
	// a no-op unless runID actually has a Waiting/conversation exec (see
	// resolveConversationWait's own guard).
	if err := e.resolveConversationWait(ctx, runID, true); err != nil {
		return counts, fmt.Errorf("resolving wait: conversation backlog: %w", err)
	}

	return counts, nil
}

// reconcileEffectNode implements 06-durability.md's "for each Effect in
// Attempted with no result" recovery step: an actor: effect node's
// EXECUTING status is never resolved by process-liveness probing (a git
// or gh subprocess has almost certainly already exited by the time a
// restart happens — what matters is whether the EXTERNAL mutation
// landed, not whether the local process is still running), so this
// bypasses the generic proc-record verdict switch entirely.
func (e *Engine) reconcileEffectNode(ctx context.Context, runID, nodeID string, exec domain.NodeExecution) (reconcileCounts, error) {
	var counts reconcileCounts

	envs, err := e.store.Read(ctx, runID)
	if err != nil {
		return counts, fmt.Errorf("reading stream %s: %w", runID, err)
	}
	var attempted *domain.EffectAttempted
	resolved := false
	for _, env := range envs {
		switch ev := env.Event.(type) {
		case domain.EffectAttempted:
			if ev.ExecID == exec.ExecID {
				a := ev
				attempted = &a
				resolved = false
			}
		case domain.EffectApplied:
			if ev.ExecID == exec.ExecID {
				resolved = true
			}
		case domain.EffectFailed:
			if ev.ExecID == exec.ExecID {
				resolved = true
			}
		}
	}

	if attempted == nil {
		// Never got as far as recording EffectAttempted — nothing was
		// externally attempted, so this is a plain crash-before-work-
		// started case, safe to treat like any other Lost node (retry
		// from scratch via the normal domain retry-or-route ladder).
		definitionRef, err := e.firstEventDefinitionRef(ctx, runID)
		if err != nil {
			return counts, err
		}
		state, ok, err := e.store.GetRunState(ctx, runID)
		if err != nil || !ok {
			return counts, err
		}
		if err := e.recoverLost(ctx, definitionRef, state, runID, nodeID, exec); err != nil {
			return counts, err
		}
		counts.lost++
		return counts, nil
	}
	if resolved {
		// Attempted AND already resolved (a live run that folded the
		// outcome, then crashed before EngineStopped) — nothing to do.
		return counts, nil
	}

	nd, ok := e.resolveNode(e.mustDefinitionRef(ctx, runID), nodeID)
	if !ok {
		return counts, fmt.Errorf("node %s not found for effect reconciliation", nodeID)
	}
	provider, ok := e.effectProviders[attempted.Effect]
	if !ok {
		return counts, fmt.Errorf("no builtin provider registered for effect %q", attempted.Effect)
	}
	workDir, err := e.workspaceRepoDirFor(ctx, runID)
	if err != nil {
		return counts, err
	}
	req := effect.Request{
		RunID: runID, NodeID: nodeID, ExecID: exec.ExecID,
		Effect: attempted.Effect, IdempotencyKey: attempted.IdempotencyKey,
		WorkDir: workDir, Dir: e.scratchDir(runID, exec.ExecID),
		Args: nd.With,
	}
	res, ok, err := provider.Probe(ctx, req)
	if err != nil {
		return counts, fmt.Errorf("probing effect %q: %w", attempted.Effect, err)
	}
	if !ok {
		// The provider genuinely cannot say — record effect.unknown and
		// leave the NodeExecution non-terminal. This IS "blocks the run
		// reaching Failed": no NodeExecutionFailed/NodeOutputReceived
		// fold happens, so the RunState can never reach a terminal
		// status while this row stays Executing (domain.RunState.Terminal
		// is derived from every NodeExecution's own terminality).
		if err := e.appendNext(ctx, runID, domain.EffectUnknown{RunID: runID, NodeID: nodeID, ExecID: exec.ExecID, Effect: attempted.Effect}); err != nil {
			return counts, err
		}
		return counts, nil
	}

	switch res.Outcome {
	case effect.Applied:
		if err := e.appendNext(ctx, runID, domain.EffectApplied{RunID: runID, NodeID: nodeID, ExecID: exec.ExecID, Effect: attempted.Effect, ExternalRef: res.ExternalRef}); err != nil {
			return counts, err
		}
		out, err := json.Marshal(map[string]any{"externalRef": res.ExternalRef})
		if err != nil {
			return counts, fmt.Errorf("marshalling effect output: %w", err)
		}
		ev := domain.NodeOutputReceived{RunID: runID, NodeID: nodeID, ExecID: exec.ExecID, SchemaValid: true, Output: json.RawMessage(out)}
		if err := e.appendAndFoldBeforeStart(ctx, runID, ev); err != nil {
			return counts, err
		}
	case effect.Failed:
		if err := e.appendNext(ctx, runID, domain.EffectFailed{RunID: runID, NodeID: nodeID, ExecID: exec.ExecID, Effect: attempted.Effect, Reason: res.Reason}); err != nil {
			return counts, err
		}
		ev := domain.NodeExecutionFailed{RunID: runID, NodeID: nodeID, ExecID: exec.ExecID, Reason: domain.FailFailure, Message: res.Reason}
		if err := e.appendAndFoldBeforeStart(ctx, runID, ev); err != nil {
			return counts, err
		}
	}
	counts.recovered++
	return counts, nil
}

// appendAndFoldBeforeStart captures runID's state, appends ev, and folds
// +dispatches it against the PRE-append state — Reconcile always runs
// before Start's live Subscribe loop exists, so nothing else will ever
// fold+dispatch ev's resulting Cmds otherwise (see parkForEffectConfirmation's
// doc comment in effect.go on why the state must be captured before, not
// after, the append).
func (e *Engine) appendAndFoldBeforeStart(ctx context.Context, runID string, ev domain.Event) error {
	preState, ok, err := e.store.GetRunState(ctx, runID)
	if err != nil {
		return fmt.Errorf("loading run state for %s: %w", runID, err)
	}
	if !ok {
		preState = domain.RunState{}
	}
	if err := e.appendNext(ctx, runID, ev); err != nil {
		return err
	}
	return e.foldAndDispatch(ctx, runID, preState, ev)
}

// recoverLost appends NodeExecutionLost for exec and, unless the node's
// resolved RestartPolicy says otherwise, dispatches whatever Cmds
// domain.Advance returns for it (a retry attempt, or nothing if retries
// were already exhausted and the run routed to $fail) — the concrete
// meaning of "re-dispatch every command with no recorded outcome" for
// this one row.
//
// RestartPolicy gates dispatch, not the fold: domain.Advance always
// computes the same retry-or-route decision regardless of policy (it has
// no notion of "fail-to-human" — that is an engine-level concern read off
// the Definition, per registry.RestartPolicy's doc comment). When policy
// is fail-to-human, a computed retry Cmd is deliberately left
// undispatched: the fresh NodeExecution row domain.Advance already
// created stays Pending forever, with no NodeExecutionStarted to move it
// — the concrete, reachable meaning of "parks after restart: stops
// automatic progress, awaiting a human" for L05, since domain has no
// direct Lost-to-Parked transition (ExecParked is reached only via the
// separate loopGuard-exceeded path today).
func (e *Engine) recoverLost(ctx context.Context, definitionRef string, state domain.RunState, runID, nodeID string, exec domain.NodeExecution) error {
	now := time.Now().UTC()
	ev := domain.NodeExecutionLost{RunID: runID, NodeID: nodeID, ExecID: exec.ExecID}

	if err := e.appendNext(ctx, runID, ev); err != nil {
		return fmt.Errorf("appending node.execution.lost: %w", err)
	}

	_, cmds, err := domain.Advance(state, ev, now)
	if err != nil {
		return fmt.Errorf("folding node.execution.lost: %w", err)
	}

	policy := e.restartPolicyFor(definitionRef, nodeID)
	if policy == registry.RestartFailToHuman && len(cmds) > 0 {
		e.log.Warn("node lost at boot; restartPolicy=fail-to-human, not auto-retrying (no human queue yet, L13)",
			"runID", runID, "nodeID", nodeID, "execID", exec.ExecID)
		return nil
	}

	for _, cmd := range cmds {
		if err := e.dispatch(ctx, definitionRef, cmd); err != nil {
			return fmt.Errorf("dispatching recovery cmd: %w", err)
		}
	}
	return nil
}

// restartPolicyFor loads the Definition to resolve nodeID's RestartPolicy.
// A load failure defaults to fail-to-human — the safer failure mode when
// the policy itself can't be determined.
func (e *Engine) restartPolicyFor(definitionRef, nodeID string) registry.RestartPolicy {
	nd, ok := e.resolveNode(definitionRef, nodeID)
	if !ok {
		return registry.RestartFailToHuman
	}
	return nd.RestartPolicy
}

// resolveNode loads definitionRef and finds nodeID's full NodeDef.
func (e *Engine) resolveNode(definitionRef, nodeID string) (registry.NodeDef, bool) {
	def, err := e.loadDefinition(definitionRef)
	if err != nil {
		return registry.NodeDef{}, false
	}
	for _, nd := range def.Nodes {
		if string(nd.ID) == nodeID {
			return nd, true
		}
	}
	return registry.NodeDef{}, false
}

// adoptWatch is what RestartPolicy: adopt buys a node that survived a
// daemon crash: instead of killing the orphan and losing its work,
// reconciliation leaves it running and this goroutine polls its identity
// (bootID+pgid, never a bare pid — the same check Probe uses) until it
// exits, then folds the real outcome through the normal output/failure
// path exactly as if a live engine had Wait()ed on it — no
// NodeExecutionLost, no retry, because nothing was ever lost.
func (e *Engine) adoptWatch(ctx context.Context, nd registry.NodeDef, runID, nodeID, execID string, rec local.ProcRecord, bootID string) {
	dir := e.scratchDir(runID, execID)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if local.Probe(rec, bootID) == local.VerdictAlive {
				continue
			}
			if body, ok := readOutputIfAny(dir); ok {
				_ = e.appendNext(ctx, runID, domain.NodeOutputReceived{
					RunID: runID, NodeID: nodeID, ExecID: execID,
					SchemaValid: validateOutput(nd, body), Output: body,
				})
			} else {
				_ = e.appendNodeFailed(ctx, runID, nodeID, execID, domain.FailFailure, "adopted process exited with no output.json")
			}
			return
		}
	}
}
