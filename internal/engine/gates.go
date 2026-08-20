package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/williamokano/kairos/internal/artifact"
	"github.com/williamokano/kairos/internal/constraint"
	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/registry"
)

// evaluateGates replaces L05's WARN-logged placeholder (decision #5 in
// L05-engine.md) with real evaluation of the node's declared gate
// schedule, in the fixed order 05-gates.md's invariant specifies:
// output-schema validation (already done by the actor dispatch functions
// before NodeOutputReceived is ever appended) -> gates in declared order
// -> on edges (domain's own job, driven by the NodeGatesEvaluated this
// function appends). `strategy: all` (05-gates.md's local default) is
// unconditional here: every declared gate runs regardless of an earlier
// one failing, and every failing gate's findings are collected into one
// NodeGatesEvaluated — one round trip beats N, and the constitution-level
// `strategy: fail-fast` override is L11 (policy) scope, not this one's.
func (e *Engine) evaluateGates(ctx context.Context, def registry.Definition, c domain.CmdEvaluateGates) error {
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
		return fmt.Errorf("engine: node %q not found in definition for gate evaluation", c.NodeID)
	}
	if len(nd.Gates) == 0 {
		return e.appendNext(ctx, c.RunID, domain.NodeGatesEvaluated{
			RunID: c.RunID, NodeID: c.NodeID, ExecID: c.ExecID, Passed: true,
		})
	}

	output, err := e.readExecOutput(ctx, c.RunID, c.NodeID, c.ExecID)
	if err != nil {
		return fmt.Errorf("reading output for gate evaluation: %w", err)
	}

	workDir := e.scratchDir(c.RunID, c.ExecID)
	if nd.Workspace == registry.WorkspaceWrite && e.workspaceRepo != "" {
		// Provision is idempotent (L06) — a workspace: write node already
		// provisioned its clone in the actor dispatch that produced this
		// output; this call returns the same directory rather than
		// re-cloning.
		ws, err := e.workspaces.Provision(ctx, c.RunID, e.workspaceRepo)
		if err != nil {
			return fmt.Errorf("resolving workspace for gate evaluation: %w", err)
		}
		workDir = ws.Dir
	}

	allPassed := true
	var findings []domain.Finding
	for _, gateID := range nd.Gates {
		gd, ok := def.Gates[gateID]
		if !ok {
			// Unresolved against this document's own gates: map — the
			// real constitution merge (kairos/baseline + project + repo)
			// is L11 scope (see validate.go's matching comment). Not
			// silently ignored: a WARN names exactly what was skipped,
			// matching L05's original placeholder posture for an honest,
			// documented gap (AGENTS §4 rule 1).
			e.log.Warn("gate has no local definition (constitution resolution is L11 scope)",
				"runID", c.RunID, "nodeID", c.NodeID, "gateID", gateID)
			continue
		}

		gateDir := filepath.Join(e.scratchDir(c.RunID, c.ExecID), "gate-"+gateID)
		result, err := e.constraints.Evaluate(ctx, constraint.Input{
			Gate:    gd,
			RunID:   c.RunID,
			NodeID:  c.NodeID,
			ExecID:  c.ExecID,
			Output:  output,
			WorkDir: workDir,
			Dir:     gateDir,
		})
		if err != nil {
			return fmt.Errorf("evaluating gate %q: %w", gateID, err)
		}

		// constraint.evaluated is appended for EVERY evaluation, pass or
		// fail — 05-gates.md's "fake the result" defence: the engine
		// records the child's actual outcome, never the agent's claim.
		if err := e.appendNext(ctx, c.RunID, domain.ConstraintEvaluated{
			RunID: c.RunID, NodeID: c.NodeID, ExecID: c.ExecID,
			GateID: gateID, Kind: string(gd.Kind), Passed: result.Passed,
			ExitCode: result.ExitCode, DurationMs: result.DurationMs, Reason: result.Reason,
		}); err != nil {
			return fmt.Errorf("recording constraint.evaluated for %q: %w", gateID, err)
		}

		if !result.Passed {
			allPassed = false
			// Waivable: false is enforced by omission, not by a check
			// here: there is no waiver.grant code path anywhere in this
			// engine (L11 scope) that could turn result.Passed back to
			// true after the fact. A gate's Waivable field exists today
			// only to be validated as a declared invariant — see
			// L10-constraints-gates.md's Documented decisions and
			// TestEvaluateGates_waivableFalseGateFailureIsNeverSilentlyPassed.
			findings = append(findings, result.Findings...)
		}
	}

	return e.appendNext(ctx, c.RunID, domain.NodeGatesEvaluated{
		RunID: c.RunID, NodeID: c.NodeID, ExecID: c.ExecID, Passed: allPassed, Findings: findings,
	})
}

// readExecOutput recovers the typed JSON output a prior NodeOutputReceived
// or NodeWaitResolved recorded for this exec — CmdEvaluateGates itself
// carries only RunID/NodeID/ExecID (domain.Graph-style routing fields,
// matching CmdStartNode's own minimalism), so the output has to be read
// back from the run's own event stream rather than threaded through the
// Cmd. Resolves an OutputRef via the artifact store exactly like
// dispatchShellActor/dispatchLLMActor's reapers do when they read it
// back, keeping this idempotent and replay-safe (AGENTS §4 rule 3): a
// crash-and-retry of gate evaluation re-reads the same recorded fact
// rather than depending on in-memory state.
func (e *Engine) readExecOutput(ctx context.Context, runID, nodeID, execID string) (map[string]any, error) {
	envs, err := e.store.Read(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("reading stream %s: %w", runID, err)
	}

	var body json.RawMessage
	var ref *domain.ArtifactRef
	found := false
	for _, env := range envs {
		switch ev := env.Event.(type) {
		case domain.NodeOutputReceived:
			if ev.NodeID == nodeID && ev.ExecID == execID {
				body, ref, found = ev.Output, ev.OutputRef, true
			}
		case domain.NodeWaitResolved:
			if ev.NodeID == nodeID && ev.ExecID == execID {
				body, ref, found = ev.Output, nil, true
			}
		}
	}
	if !found {
		return nil, fmt.Errorf("no output recorded for %s/%s", nodeID, execID)
	}

	if ref != nil {
		path := e.artifacts.Path(artifact.Ref{Hash: ref.Hash, Size: ref.Size})
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading output artifact %s: %w", ref.Hash, err)
		}
		body = data
	}

	if len(body) == 0 {
		return map[string]any{}, nil
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decoding output as an object: %w", err)
	}
	return decoded, nil
}
