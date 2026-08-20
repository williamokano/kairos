package engine

import (
	"context"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/registry"
)

// evaluateGates is L05's documented placeholder (decision #5): a node
// with no declared gates always passes; a node with declared gates also
// always passes, but a WARN names every gate that was never actually
// evaluated (AGENTS §4 rule 1: no silent fallback — a real, honest gap,
// not a shortcut). L10 replaces this with the real gate library.
func (e *Engine) evaluateGates(ctx context.Context, def registry.Definition, c domain.CmdEvaluateGates) error {
	var nd registry.NodeDef
	for _, n := range def.Nodes {
		if string(n.ID) == c.NodeID {
			nd = n
			break
		}
	}
	if len(nd.Gates) > 0 {
		e.log.Warn("gates declared but not evaluated (no gate library until L10)",
			"runID", c.RunID, "nodeID", c.NodeID, "gates", nd.Gates)
	}
	return e.appendNext(ctx, c.RunID, domain.NodeGatesEvaluated{
		RunID: c.RunID, NodeID: c.NodeID, ExecID: c.ExecID, Passed: true,
	})
}
