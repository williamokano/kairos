package registry

import "github.com/williamokano/kairos/internal/domain"

// ProjectGraph is the seam between the rich Definition and the minimal
// domain.Graph L01 defined: it emits only what domain.Advance needs to
// route a run (ID, Wait, Retry, LoopGuard, Edges). Actor, prompt, inputs,
// output schema, gates, effects, artifacts, and spawn/join detail are
// dropped here — the engine (L05) looks those up from the Definition
// itself, by TriggerReceived.DefinitionRef, when a CmdStartNode needs them.
func ProjectGraph(def Definition) (domain.Graph, error) {
	if len(def.Nodes) == 0 {
		return domain.Graph{}, nil
	}
	nodes := make([]domain.Node, 0, len(def.Nodes))
	for _, nd := range def.Nodes {
		nodes = append(nodes, projectNode(nd, def.Limits))
	}
	return domain.Graph{
		Entry: def.Nodes[0].ID,
		Nodes: nodes,
		Edges: resolveEdges(def.Nodes),
	}, nil
}

func projectNode(nd NodeDef, limits LimitsDef) domain.Node {
	return domain.Node{
		ID:        nd.ID,
		Wait:      projectWait(nd.Wait),
		Retry:     domain.RetryPolicy{MaxAttempts: nd.Retry.MaxAttempts},
		LoopGuard: domain.LoopGuard{MaxIterationsPerNode: limits.LoopGuard.MaxIterationsPerNode},
	}
}

// projectWait collapses NodeDef.Wait's possibly-multiple sources to the
// single domain.WaitKind L01's WaitSpec models (decision #4: first entry's
// kind wins; the full list stays on NodeDef.Wait.On for the engine).
// TimeoutAt stays nil (decision #3): resolving Wait.Timeout, a relative
// duration, to an absolute instant needs "now", which parsing must not
// read — the engine adds it to dispatch-time "now" when it issues
// CmdArmTimer.
func projectWait(w *WaitDef) *domain.WaitSpec {
	if w == nil {
		return nil
	}
	spec := &domain.WaitSpec{OnTimeout: domain.OnTimeoutAction(w.OnTimeout)}
	if len(w.On) > 0 {
		spec.Kind = domain.WaitKind(w.On[0].Kind)
	}
	return spec
}
