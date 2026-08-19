package domain

// RunStatus is the closed set of states a Run can occupy.
type RunStatus string

const (
	RunPending    RunStatus = "pending"
	RunRunning    RunStatus = "running"
	RunDegradedS  RunStatus = "degraded" // 03-workflows.md: a first-class state, resolvable only by a coordinator
	RunSucceeded  RunStatus = "succeeded"
	RunFailed     RunStatus = "failed"
	RunCancelledS RunStatus = "cancelled"
	RunRejectedS  RunStatus = "rejected" // preflight failed; never entered Running (06-durability.md)
)

// Terminal reports whether s is a terminal Run status.
func (s RunStatus) Terminal() bool {
	switch s {
	case RunSucceeded, RunFailed, RunCancelledS, RunRejectedS:
		return true
	case RunPending, RunRunning, RunDegradedS:
		return false
	default:
		return false
	}
}

// Valid reports whether s is one of the closed enum values.
func (s RunStatus) Valid() bool {
	switch s {
	case RunPending, RunRunning, RunDegradedS, RunSucceeded, RunFailed, RunCancelledS, RunRejectedS:
		return true
	default:
		return false
	}
}

// RunState is the full projection of a run's event stream: Advance(state,
// ev, now) folds one Event into one RunState. Executions is append-only per
// node — a retry allocates a new NodeExecution rather than mutating one.
type RunState struct {
	ID     string
	Status RunStatus
	Graph  Graph

	// Executions[nodeID] is ordered oldest-first; the last entry is the
	// current attempt/iteration for that node.
	Executions map[NodeID][]NodeExecution
}

// Terminal reports whether the run's Status is terminal.
func (s RunState) Terminal() bool {
	return s.Status.Terminal()
}

// current returns the most recent NodeExecution for id, if any.
func (s RunState) current(id NodeID) (NodeExecution, bool) {
	execs := s.Executions[id]
	if len(execs) == 0 {
		return NodeExecution{}, false
	}
	return execs[len(execs)-1], true
}

// withExecution returns a copy of s with exec appended/replaced as the
// current execution for exec.NodeID. Advance never mutates its input state.
func (s RunState) withExecution(exec NodeExecution) RunState {
	next := RunState{
		ID:         s.ID,
		Status:     s.Status,
		Graph:      s.Graph,
		Executions: make(map[NodeID][]NodeExecution, len(s.Executions)),
	}
	for k, v := range s.Executions {
		next.Executions[k] = append([]NodeExecution(nil), v...)
	}
	execs := next.Executions[exec.NodeID]
	if n := len(execs); n > 0 && execs[n-1].ExecID == exec.ExecID {
		execs[n-1] = exec
	} else {
		execs = append(execs, exec)
	}
	next.Executions[exec.NodeID] = execs
	return next
}

// withStatus returns a copy of s with Status set to status.
func (s RunState) withStatus(status RunStatus) RunState {
	s.Status = status
	return s
}
