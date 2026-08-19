package domain

import "errors"

var (
	// ErrIllegalTransition is returned when an Event would move a Run or
	// NodeExecution through a transition absent from transitions.go's
	// legal-transition tables.
	ErrIllegalTransition = errors.New("domain: illegal state transition")

	// ErrRejectedNeedsFindings is returned when NodeGatesEvaluated reports
	// Passed=false with no Findings — a rejected outcome is meaningless
	// without at least one finding to feed the retry loop's inputs.
	ErrRejectedNeedsFindings = errors.New("domain: rejected outcome requires at least one finding")

	// ErrUnresolvedEdge is returned when a Graph has no edge for a trigger
	// a node's outcome produced. Resolving every possible trigger is L03's
	// job; Advance refuses to guess.
	ErrUnresolvedEdge = errors.New("domain: graph has no edge for this trigger")

	// ErrUnknownEvent is returned when Advance is given an Event type it
	// does not recognise.
	ErrUnknownEvent = errors.New("domain: unknown event type")

	// ErrUnknownNode is returned when an Event names a NodeID absent from
	// the run's Graph.
	ErrUnknownNode = errors.New("domain: unknown node")

	// ErrNoCurrentExecution is returned when an Event refers to a
	// NodeExecution that does not exist for the named node.
	ErrNoCurrentExecution = errors.New("domain: no current execution for node")

	// ErrExecIDMismatch is returned when an Event's ExecID does not match
	// the node's current NodeExecution — a stale or duplicate fact.
	ErrExecIDMismatch = errors.New("domain: exec id does not match current execution")
)
