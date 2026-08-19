package domain

// legalExecEvents lists, for each non-terminal ExecStatus, the event types
// legal to observe in that status. A terminal ExecStatus accepts no further
// event for that same NodeExecution row — a retry always allocates a new
// one (see nodeexecution.go). Advance consults this table before running
// any per-event handler logic and returns ErrIllegalTransition otherwise.
var legalExecEvents = map[ExecStatus]map[string]bool{
	ExecPending: {
		NodeExecutionStarted{}.EventType(): true,
	},
	ExecExecuting: {
		NodeOutputReceived{}.EventType():       true,
		NodeGatesEvaluated{}.EventType():       true,
		NodeExecutionFailed{}.EventType():      true,
		NodeExecutionInterrupted{}.EventType(): true,
		NodeExecutionLost{}.EventType():        true,
		NodeExecutionAdopted{}.EventType():     true,
	},
	ExecAdopted: {
		NodeOutputReceived{}.EventType():  true,
		NodeGatesEvaluated{}.EventType():  true,
		NodeExecutionFailed{}.EventType(): true,
	},
	ExecWaiting: {
		NodeWaitResolved{}.EventType():  true,
		HumanTaskAnswered{}.EventType(): true,
	},
}

// legalExecEvent reports whether event type et is legal to observe on a
// NodeExecution currently in status s.
func legalExecEvent(s ExecStatus, et string) bool {
	byEvent, ok := legalExecEvents[s]
	if !ok {
		return false // terminal statuses accept nothing
	}
	return byEvent[et]
}

// legalRunEvents lists, for each non-terminal RunStatus, the event types
// legal to observe directly against Run-level state. RunSucceeded/Failed
// are NOT separate consumed events — they are derived, within the same
// Advance call, from a node's terminal outcome routing to a Graph sink
// (see advance.go); recording them for observers is the engine's job (L05).
var legalRunEvents = map[RunStatus]map[string]bool{
	RunPending: {
		TriggerReceived{}.EventType(): true,
		RunStarted{}.EventType():      true,
		RunRejected{}.EventType():     true,
	},
	RunRunning: {
		NodeExecutionStarted{}.EventType():     true,
		NodeOutputReceived{}.EventType():       true,
		NodeWaitResolved{}.EventType():         true,
		NodeGatesEvaluated{}.EventType():       true,
		NodeExecutionFailed{}.EventType():      true,
		NodeExecutionInterrupted{}.EventType(): true,
		NodeExecutionLost{}.EventType():        true,
		NodeExecutionAdopted{}.EventType():     true,
		HumanTaskCreated{}.EventType():         true,
		HumanTaskAnswered{}.EventType():        true,
		RunCancelled{}.EventType():             true,
		RunDegraded{}.EventType():              true,
	},
	RunDegradedS: {
		RunDegradedResolved{}.EventType(): true,
		RunCancelled{}.EventType():        true,
	},
}

func legalRunEvent(s RunStatus, et string) bool {
	byEvent, ok := legalRunEvents[s]
	if !ok {
		return false
	}
	return byEvent[et]
}
