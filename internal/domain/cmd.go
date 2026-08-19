package domain

import "time"

// Cmd is an intention Advance returns for the engine to dispatch. Each Cmd
// eventually produces a new fact Event fed back into Advance — domain never
// dispatches anything itself, it only names what should happen
// ("decision BEFORE action", 01-architecture.md).
type Cmd interface {
	isCmd()
}

// CmdStartNode asks the engine to invoke a node's actor.
type CmdStartNode struct {
	RunID, NodeID, ExecID string
	Attempt, Iteration    int
}

func (CmdStartNode) isCmd() {}

// CmdEvaluateGates asks the engine to run a node's gate schedule.
type CmdEvaluateGates struct {
	RunID, NodeID, ExecID string
}

func (CmdEvaluateGates) isCmd() {}

// CmdEnterWait asks the engine to record the wait's bookkeeping (waiters
// row, human task if Wait.Kind == human) — 06-durability.md: "a wait's
// entire footprint is three rows."
type CmdEnterWait struct {
	RunID, NodeID, ExecID string
	Wait                  WaitSpec
}

func (CmdEnterWait) isCmd() {}

// CmdCreateHumanTask asks the engine to surface a human task for a Waiting
// or Parked execution.
type CmdCreateHumanTask struct {
	RunID, NodeID, ExecID string
}

func (CmdCreateHumanTask) isCmd() {}

// CmdArmTimer asks the engine to arm a timer keyed to FireAt.
// 03-workflows.md: "onTimeout stays a required field, unpublishable without
// it" — domain's half of that invariant is that it never returns
// CmdEnterWait for a WaitSpec with a TimeoutAt set without an accompanying
// CmdArmTimer.
type CmdArmTimer struct {
	RunID, NodeID, ExecID string
	FireAt                time.Time
}

func (CmdArmTimer) isCmd() {}

// CmdSignalNode asks the engine to signal (interrupt/cancel) an in-flight
// node's process group.
type CmdSignalNode struct {
	RunID, NodeID, ExecID string
}

func (CmdSignalNode) isCmd() {}
