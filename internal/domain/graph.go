package domain

import "time"

// NodeID identifies a node within a Graph, and doubles as the two reserved
// sink IDs "$succeed" and "$fail" every resolved Graph must route to.
type NodeID string

const (
	SinkSucceed NodeID = "$succeed"
	SinkFail    NodeID = "$fail"
)

// EdgeTrigger is an outcome a NodeExecution can reach, each of which a
// resolved Graph must route somewhere (03-workflows.md: "every outcome
// routes somewhere" is a structural invariant, not an authoring convention).
type EdgeTrigger string

const (
	OnSuccess  EdgeTrigger = "success"
	OnFailure  EdgeTrigger = "failure"
	OnTimeout  EdgeTrigger = "timeout"
	OnDenied   EdgeTrigger = "denied"
	OnRejected EdgeTrigger = "rejected"
)

// Graph is a minimal, YAML-agnostic, fully-resolved workflow shape. Every
// default a workflow author may omit (document-order edges, rejected->self,
// failure/timeout/denied->$fail, inferred input schemas) must already be
// applied by the layer that builds a Graph (L03) — domain never infers a
// default and never parses YAML (AGENTS.md §2: no path/filepath, no I/O).
type Graph struct {
	// Entry is the first node dispatched when a run starts.
	Entry NodeID
	Nodes []Node
	// Edges[nodeID][trigger] names the next NodeID, or one of the two sinks.
	// A resolved Graph MUST have an entry for every EdgeTrigger a node's
	// gates/actor can possibly produce, or Advance returns ErrUnresolvedEdge.
	Edges map[NodeID]map[EdgeTrigger]NodeID
}

// NodeByID returns the Node with the given ID and whether it was found.
func (g Graph) NodeByID(id NodeID) (Node, bool) {
	for _, n := range g.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return Node{}, false
}

// edge resolves the next NodeID for a (node, trigger) pair.
func (g Graph) edge(id NodeID, trigger EdgeTrigger) (NodeID, bool) {
	byTrigger, ok := g.Edges[id]
	if !ok {
		return "", false
	}
	next, ok := byTrigger[trigger]
	return next, ok
}

// Node is a single unit of work in a Graph. Wait, Retry, and LoopGuard are
// static, pre-resolved policy — no defaulting happens inside domain.
type Node struct {
	ID        NodeID
	Wait      *WaitSpec // non-nil: Pending->Waiting directly, never Executing
	Retry     RetryPolicy
	LoopGuard LoopGuard
}

// WaitKind names what a Waiting NodeExecution is waiting on
// (03-workflows.md: human | timer | poll | child-run | conversation).
type WaitKind string

const (
	WaitHuman        WaitKind = "human"
	WaitTimer        WaitKind = "timer"
	WaitPoll         WaitKind = "poll"
	WaitChildRun     WaitKind = "child-run"
	WaitConversation WaitKind = "conversation"
)

// OnTimeoutAction is what happens when a wait's TimeoutAt passes.
// 03-workflows.md: onTimeout is a REQUIRED field wherever wait is used —
// enforcing that is L03's publish-time job; domain enforces only that a
// WaitSpec with a TimeoutAt always produces an armed timer (see advance.go).
type OnTimeoutAction string

const (
	OnTimeoutEscalate OnTimeoutAction = "escalate"
	OnTimeoutPark     OnTimeoutAction = "park"
)

// WaitSpec is the static wait declaration attached to a Node.
type WaitSpec struct {
	Kind      WaitKind
	TimeoutAt *time.Time // nil means no timeout is armed for this wait
	OnTimeout OnTimeoutAction
}

// RetryPolicy bounds the Attempt counter, driven by failure/timeout/
// schema-invalid outcomes (03-workflows.md's `retry:` block).
type RetryPolicy struct {
	MaxAttempts int
}

// LoopGuard bounds the Iteration counter, driven by rejected outcomes
// (03-workflows.md's `on.rejected -> self`, bounded by loopGuard).
type LoopGuard struct {
	MaxIterationsPerNode int
}
