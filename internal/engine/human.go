package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/registry"
)

// dispatchCreateHumanTask makes CmdCreateHumanTask real: append
// HumanTaskCreated so the task genuinely exists in the log (AGENTS §4
// rule 1 — this used to be a WARN-only no-op). It is reachable for two
// distinct NodeExecution shapes: an ExecWaiting node whose wait.on names
// kind: human (the human-approval pattern, answerable via AnswerHumanTask
// below) and an ExecParked node (loop-guard-exceeded or
// wait-timeout-escalate) — Parked is a terminal ExecStatus with no legal
// HumanTaskAnswered transition today (see internal/domain/transitions.go),
// so a Parked task is visible but not yet answerable from here; resolving
// one requires an operator action this document does not build (see
// L13-human-decisions.md's Future work).
func (e *Engine) dispatchCreateHumanTask(ctx context.Context, c domain.CmdCreateHumanTask) error {
	return e.appendNext(ctx, c.RunID, domain.HumanTaskCreated(c))
}

// AnswerDecision is one kairos-approve-shaped answer to a Waiting(human)
// node. TypedWord is only consulted (and only required) when the node's
// resolved wait.weight is WeightType.
type AnswerDecision struct {
	Decision  string
	Reason    string
	TypedWord string
}

// ErrHumanDecisionReasonRequired is returned when Reason is empty —
// 09-cli-and-tui.md: "--reason required for every decision, not just
// negative ones."
var ErrHumanDecisionReasonRequired = fmt.Errorf("engine: a reason is required for every human decision")

// ErrHumanDecisionTypedWordRequired is returned when the node's declared
// wait.weight is "type" and the caller did not supply (or mismatched) the
// typed decision word.
var ErrHumanDecisionTypedWordRequired = fmt.Errorf("engine: this decision requires typing the node id as the confirming word (wait.weight: type)")

// AnswerHumanTask resolves an ExecWaiting(human) NodeExecution: it is the
// one and only path HumanTaskAnswered is ever appended from, so every
// anti-rubber-stamp rule this document builds lives here, not scattered
// across API/CLI callers. The weight/tier is read from the node's OWN
// declaration (registry.NodeDef.Wait.Weight) — never accepted as a
// parameter from ans — which is what makes "an actor cannot choose its
// own tier" (05-gates.md) a real invariant rather than a convention: there
// is no argument this function accepts that could weaken it.
func (e *Engine) AnswerHumanTask(ctx context.Context, runID, nodeID string, ans AnswerDecision) error {
	if ans.Reason == "" {
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
		return fmt.Errorf("engine: node %s is not currently waiting on a human decision (status %s)", nodeID, exec.Status)
	}

	definitionRef, err := e.firstEventDefinitionRef(ctx, runID)
	if err != nil {
		return fmt.Errorf("resolving definitionRef for %s: %w", runID, err)
	}
	nd, ok := e.resolveNode(definitionRef, nodeID)
	if !ok {
		return fmt.Errorf("node %s not found in definition for run %s", nodeID, runID)
	}
	if nd.Wait == nil || !waitsOnHuman(nd.Wait) {
		return fmt.Errorf("engine: node %s is not a wait: human node", nodeID)
	}

	if err := checkDecisionWeight(nd, nodeID, ans); err != nil {
		return err
	}

	body, err := json.Marshal(map[string]string{"decision": ans.Decision, "reason": ans.Reason})
	if err != nil {
		return fmt.Errorf("marshalling human decision output: %w", err)
	}
	valid := validateOutput(nd, body)

	ev := domain.HumanTaskAnswered{
		RunID: runID, NodeID: nodeID, ExecID: exec.ExecID,
		Output: json.RawMessage(body), SchemaValid: valid,
	}
	// Append only — do NOT also fold+dispatch the resulting CmdEvaluateGates
	// here. AnswerHumanTask is only ever reachable while the engine is
	// live (Reconcile, then Start, then the API binds — see
	// cmd/kairos/serve.go's boot order), so this event lands on runID's
	// own stream, which the live Subscribe loop is already watching; the
	// owning shard picks it up and dispatches it. Doing so here too would
	// run it twice — the exact double-dispatch race
	// resolveConversationWait's dispatchCmds=false already documents and
	// avoids for wait: conversation; this had the identical bug until a
	// full-suite -race run caught it (two concurrent callers folding the
	// same HumanTaskAnswered independently, the second landing on a
	// NodeExecution the first had already advanced past).
	return e.appendNext(ctx, runID, ev)
}

// checkDecisionWeight enforces 05-gates.md's "decision weight must match
// reversibility" table at the one tier this document can actually test
// without a TUI: WeightType's typed-word requirement. silent/glance/read
// have no additional CLI-layer requirement beyond the reason that's
// already mandatory for every decision — their distinction is a rendering
// concern (batching, single keypress vs. full evidence) that belongs to
// the TUI/web document that doesn't exist yet (see Future work).
func waitsOnHuman(wd *registry.WaitDef) bool {
	for _, src := range wd.On {
		if src.Kind == registry.WaitHuman {
			return true
		}
	}
	return false
}

func checkDecisionWeight(nd registry.NodeDef, nodeID string, ans AnswerDecision) error {
	if nd.Wait.Weight != registry.WeightType {
		return nil
	}
	if ans.TypedWord != nodeID {
		return ErrHumanDecisionTypedWordRequired
	}
	return nil
}
