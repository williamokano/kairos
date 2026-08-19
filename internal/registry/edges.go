package registry

import "github.com/williamokano/kairos/internal/domain"

// resolveEdges applies 03-workflows.md's document-order defaulting: every
// node gets an edge for every trigger domain.Advance can observe, so
// ProjectGraph's result never trips domain.ErrUnresolvedEdge.
//
//   - success:  the author's on.success override, else the next node in
//     document order, else $succeed for the last node.
//   - failure/timeout/denied: the author's override, else $fail.
//     denied is defaulted now even though nothing through L02 can yet
//     produce it (decision #1) — leaving it unresolved would break every
//     published workflow the moment L11/L12 starts emitting it.
//   - rejected: the author's override, else the node itself (the
//     findings-loop default), bounded at runtime by LoopGuard.
func resolveEdges(nodes []NodeDef) map[domain.NodeID]map[domain.EdgeTrigger]domain.NodeID {
	edges := make(map[domain.NodeID]map[domain.EdgeTrigger]domain.NodeID, len(nodes))
	for i, n := range nodes {
		successDefault := domain.SinkSucceed
		if i < len(nodes)-1 {
			successDefault = nodes[i+1].ID
		}
		edges[n.ID] = map[domain.EdgeTrigger]domain.NodeID{
			domain.OnSuccess:  edgeOrDefault(n.On, domain.OnSuccess, successDefault),
			domain.OnFailure:  edgeOrDefault(n.On, domain.OnFailure, domain.SinkFail),
			domain.OnTimeout:  edgeOrDefault(n.On, domain.OnTimeout, domain.SinkFail),
			domain.OnDenied:   edgeOrDefault(n.On, domain.OnDenied, domain.SinkFail),
			domain.OnRejected: edgeOrDefault(n.On, domain.OnRejected, n.ID),
		}
	}
	return edges
}

func edgeOrDefault(overrides map[domain.EdgeTrigger]domain.NodeID, trigger domain.EdgeTrigger, def domain.NodeID) domain.NodeID {
	if overrides != nil {
		if v, ok := overrides[trigger]; ok {
			return v
		}
	}
	return def
}
