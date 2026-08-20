package domain

// ExecStatus is the closed set of states a NodeExecution can occupy. A
// NodeExecution that reaches a Terminal status is never mutated again —
// "retry" always allocates a new NodeExecution with PriorExecID set,
// satisfying L2 (events are immutable) at the aggregate level.
type ExecStatus string

const (
	ExecPending     ExecStatus = "pending"
	ExecExecuting   ExecStatus = "executing"
	ExecWaiting     ExecStatus = "waiting"
	ExecAdopted     ExecStatus = "adopted" // legal from L01; only reachable starting L06
	ExecSucceeded   ExecStatus = "succeeded"
	ExecFailed      ExecStatus = "failed"
	ExecRejected    ExecStatus = "rejected"
	ExecInterrupted ExecStatus = "interrupted"
	ExecLost        ExecStatus = "lost"
	ExecParked      ExecStatus = "parked"
)

// Terminal reports whether s is a terminal status: no further transition is
// legal, and the owning NodeExecution row is never mutated again.
func (s ExecStatus) Terminal() bool {
	switch s {
	case ExecSucceeded, ExecFailed, ExecRejected, ExecInterrupted, ExecLost, ExecParked:
		return true
	case ExecPending, ExecExecuting, ExecWaiting, ExecAdopted:
		return false
	default:
		return false
	}
}

// Valid reports whether s is one of the closed enum values.
func (s ExecStatus) Valid() bool {
	switch s {
	case ExecPending, ExecExecuting, ExecWaiting, ExecAdopted,
		ExecSucceeded, ExecFailed, ExecRejected, ExecInterrupted, ExecLost, ExecParked:
		return true
	default:
		return false
	}
}

// FailReason narrows why a NodeExecution reached ExecFailed.
type FailReason string

const (
	FailNone          FailReason = ""
	FailFailure       FailReason = "failure"
	FailTimeout       FailReason = "timeout"
	FailSchemaInvalid FailReason = "schema-invalid"
	FailCancelled     FailReason = "cancelled"
	// FailPolicyDenied covers both a deny-tier effect and a confirm-tier
	// effect with no recorded EffectConfirmed — L11's
	// ~/.kairos/policy.yaml enforcement. The distinguishing detail lives
	// in NodeExecutionFailed.Message, not a further FailReason split,
	// matching FailFailure's existing precedent of one reason plus a
	// human-readable message.
	FailPolicyDenied FailReason = "policy-denied"
)

// ParkReason narrows why a NodeExecution reached ExecParked. Three distinct
// doc-cited triggers converge on one status because they mean the same
// thing operationally: stop automatic progress, surface a human task.
type ParkReason string

const (
	ParkNone                ParkReason = ""
	ParkNonIdempotentAtBoot ParkReason = "non-idempotent-at-boot" // 12-build-plan.md restartPolicy: fail-to-human
	ParkWaitTimeoutEscalate ParkReason = "wait-timeout-escalate"  // 03-workflows.md onTimeout: escalate
	ParkLoopGuardExceeded   ParkReason = "loop-guard-exceeded"    // 03-workflows.md loopGuard.onExceeded: escalate-to-human
)

// Finding is the minimal shape a rejected gate outcome carries. L10 (gates)
// is the real producer and may add fields; the aggregate verdict shape
// (Passed bool, []Finding) established in event.go must not change.
type Finding struct {
	ID       string
	Message  string
	Severity string
}

// NodeExecution is one attempt (or wait, or park) at running a Node.
// PriorExecID chains retries/iterations for lineage; it is empty for a
// node's first execution.
type NodeExecution struct {
	ExecID      string
	PriorExecID string
	NodeID      NodeID
	Status      ExecStatus

	Attempt   int // bounded by Node.Retry.MaxAttempts; driven by failure/timeout/schema-invalid
	Iteration int // bounded by Node.LoopGuard.MaxIterationsPerNode; driven by rejected

	// Overdue is set when a Waiting execution's WaitSpec.TimeoutAt passes
	// and OnTimeout == park. It causes NO status transition — the
	// execution stays ExecWaiting (03-workflows.md: "it never proceeds
	// and never fails, it just waits and shows a badge").
	Overdue bool

	Reason     FailReason // set when Status == ExecFailed
	ParkReason ParkReason // set when Status == ExecParked
	Findings   []Finding  // set when Status == ExecRejected; len > 0 required
}
