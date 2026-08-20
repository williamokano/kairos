package engine

import (
	"context"
	"fmt"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/executor/local"
)

// SetPaused flips L19's "close the lid" flag. true stops every new
// CmdStartNode from being admitted (see admitOrQueue's check) — a node
// already Executing is left alone, so a run parks at its next node
// boundary rather than being interrupted. false drains whatever queued up
// while paused through real admission, exactly like a release would.
func (e *Engine) SetPaused(ctx context.Context, paused bool) {
	e.paused.Store(paused)
	if !paused {
		e.drainMu.Lock()
		items := e.decidePendingLocked()
		e.drainMu.Unlock()
		e.dispatchDrained(ctx, items)
	}
}

// Paused reports the current pause state — GET /status surfaces this.
func (e *Engine) Paused() bool { return e.paused.Load() }

// InFlightCount is the number of node executions currently holding
// admission claims — a live proxy for "how many nodes are actually
// running right now," used by `kairos park --wait` to know when it is
// safe to report the run fully parked. A rule-actor node holds its claim
// for the length of one synchronous call, so in practice this reflects
// shell/llm/effect actors with real, observable duration.
func (e *Engine) InFlightCount() int {
	e.claimsMu.Lock()
	defer e.claimsMu.Unlock()
	return len(e.claims)
}

// SelfCheckReport is `kairos doctor --self-check`'s result: the daemon's
// own event-log integrity plus the two live-operation invariants L06/L19
// exist to guarantee — no NodeExecution the log calls Executing without a
// genuinely alive, identity-matched process behind it, and no workspace
// directory left behind for a run that no longer exists.
type SelfCheckReport struct {
	DBClean                 bool
	MismatchedRunIDs        []string
	UnverifiableExecutions  []string // "runID/nodeID" pairs — see doc comment on SelfCheck
	OrphanWorkspacesRemoved []string
}

// SelfCheck is a live, on-demand health check — distinct from Reconcile,
// which only ever runs once at boot. It never kills a process (that is
// reboot-time Reconcile's job, gated on a changed bootID before any
// signal per 06-durability.md's identity rules) — during live operation a
// process this check finds unverifiable is a genuine anomaly (the owning
// shard should have reaped it itself), so this reports rather than acts,
// matching AGENTS §4 rule 1's "never silently accept" without also
// silently guessing it's safe to kill something the live engine didn't
// itself decide to kill. Workspace GC, by contrast, is always safe to run
// live (an orphan workspace directory has no owning run by definition),
// so this check performs it rather than merely reporting it.
func (e *Engine) SelfCheck(ctx context.Context) (SelfCheckReport, error) {
	var report SelfCheckReport

	verify, err := e.store.Verify(ctx)
	if err != nil {
		return report, fmt.Errorf("verifying event log: %w", err)
	}
	report.MismatchedRunIDs = verify.MismatchedRunIDs
	report.DBClean = len(verify.MismatchedRunIDs) == 0

	bootID, err := e.bootID.Current()
	if err != nil {
		return report, fmt.Errorf("reading boot id: %w", err)
	}

	runs, err := e.store.ListRuns(ctx, nil)
	if err != nil {
		return report, fmt.Errorf("listing runs: %w", err)
	}
	activeRunIDs := map[string]bool{}
	for _, r := range runs {
		if r.Status.Terminal() {
			continue
		}
		activeRunIDs[r.RunID] = true

		state, ok, err := e.store.GetRunState(ctx, r.RunID)
		if err != nil || !ok {
			continue
		}
		for nodeID, execs := range state.Executions {
			if len(execs) == 0 {
				continue
			}
			exec := execs[len(execs)-1]
			if exec.Status != domain.ExecExecuting {
				continue
			}
			dir := e.scratchDir(r.RunID, exec.ExecID)
			rec, ok, err := local.ReadProcRecord(dir)
			verdict := local.VerdictUnverifiable
			if err == nil && ok {
				verdict = local.Probe(rec, bootID)
			}
			if verdict != local.VerdictAlive {
				report.UnverifiableExecutions = append(report.UnverifiableExecutions,
					r.RunID+"/"+string(nodeID))
			}
		}
	}

	removed, err := e.workspaces.GC(ctx, activeRunIDs)
	if err != nil {
		return report, fmt.Errorf("collecting orphan workspaces: %w", err)
	}
	report.OrphanWorkspacesRemoved = removed

	return report, nil
}
