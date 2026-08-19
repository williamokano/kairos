package registry

import (
	"testing"

	"github.com/williamokano/kairos/internal/domain"
)

func TestResolveEdges_documentOrderWithRejectedSelfLoop(t *testing.T) {
	nodes := []NodeDef{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	edges := resolveEdges(nodes)

	if edges["a"][domain.OnSuccess] != "b" {
		t.Errorf("a.success = %s, want b", edges["a"][domain.OnSuccess])
	}
	if edges["b"][domain.OnSuccess] != "c" {
		t.Errorf("b.success = %s, want c", edges["b"][domain.OnSuccess])
	}
	if edges["c"][domain.OnSuccess] != domain.SinkSucceed {
		t.Errorf("c.success = %s, want $succeed (last node)", edges["c"][domain.OnSuccess])
	}
	for _, id := range []domain.NodeID{"a", "b", "c"} {
		if edges[id][domain.OnFailure] != domain.SinkFail {
			t.Errorf("%s.failure = %s, want $fail", id, edges[id][domain.OnFailure])
		}
		if edges[id][domain.OnTimeout] != domain.SinkFail {
			t.Errorf("%s.timeout = %s, want $fail", id, edges[id][domain.OnTimeout])
		}
		if edges[id][domain.OnDenied] != domain.SinkFail {
			t.Errorf("%s.denied = %s, want $fail (decision #1: defaulted now)", id, edges[id][domain.OnDenied])
		}
		if edges[id][domain.OnRejected] != id {
			t.Errorf("%s.rejected = %s, want self", id, edges[id][domain.OnRejected])
		}
	}
}

func TestResolveEdges_authorOverrideWins(t *testing.T) {
	nodes := []NodeDef{
		{ID: "a", On: map[domain.EdgeTrigger]domain.NodeID{domain.OnSuccess: "c"}},
		{ID: "b"},
		{ID: "c"},
	}
	edges := resolveEdges(nodes)
	if edges["a"][domain.OnSuccess] != "c" {
		t.Errorf("a.success = %s, want c (author override, skipping document-order default of b)", edges["a"][domain.OnSuccess])
	}
}
