package engine

import (
	"context"
	"encoding/json"

	"github.com/williamokano/kairos/internal/domain"
)

// dispatchRuleActor is L05's documented placeholder for actor: "rule"
// (decision #3): a trivial, always-succeeds, in-process, zero-subprocess
// actor. "rule" appears nowhere in 03-workflows.md's actor enum — it
// exists only in 12-build-plan.md's milestone spec, where n1's entire job
// is to exist and succeed so the milestone workflow has a first node with
// no process to reason about. No rule expression language exists or is
// implied; this is registered as a named gap, not a feature.
func (e *Engine) dispatchRuleActor(ctx context.Context, c domain.CmdStartNode) error {
	if err := e.appendNext(ctx, c.RunID, domain.NodeExecutionStarted(c)); err != nil {
		return err
	}
	return e.appendNext(ctx, c.RunID, domain.NodeOutputReceived{
		RunID: c.RunID, NodeID: c.NodeID, ExecID: c.ExecID, SchemaValid: true, Output: json.RawMessage(`{}`),
	})
}
