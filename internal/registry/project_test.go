package registry

import (
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/domain"
)

// TestProjectGraph_fixIssueMatchesHandRolledGraph drives the real README
// fix-issue.yaml, parsed and projected with no hand-authored domain.Graph,
// through a full run via domain.Advance to RunSucceeded — the same
// "no consumer, proven by tests" pattern L01/L02 used, and the real proof
// this layer is usable end-to-end structurally.
func TestProjectGraph_fixIssueMatchesHandRolledGraph(t *testing.T) {
	def, err := Load("testdata/fix-issue.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	graph, err := ProjectGraph(def)
	if err != nil {
		t.Fatalf("ProjectGraph: %v", err)
	}
	if graph.Entry != "plan" {
		t.Fatalf("Entry = %s, want plan", graph.Entry)
	}
	if len(graph.Nodes) != 4 {
		t.Fatalf("len(Nodes) = %d, want 4", len(graph.Nodes))
	}

	now := time.Unix(0, 0)
	runID := "run_1"

	state, _, err := domain.Advance(domain.RunState{}, domain.TriggerReceived{RunID: runID}, now)
	if err != nil {
		t.Fatalf("TriggerReceived: %v", err)
	}
	state, cmds, err := domain.Advance(state, domain.RunStarted{RunID: runID, Graph: graph}, now)
	if err != nil {
		t.Fatalf("RunStarted: %v", err)
	}

	order := []domain.NodeID{"plan", "implement", "approve", "pr"}
	for _, nodeID := range order {
		start, ok := cmds[0].(domain.CmdStartNode)
		if !ok {
			t.Fatalf("expected CmdStartNode for %s, got %v", nodeID, cmds)
		}
		if start.NodeID != string(nodeID) {
			t.Fatalf("dispatched node = %s, want %s", start.NodeID, nodeID)
		}

		state, _, err = domain.Advance(state, domain.NodeExecutionStarted{
			RunID: runID, NodeID: string(nodeID), ExecID: start.ExecID, Attempt: 1, Iteration: 1,
		}, now)
		if err != nil {
			t.Fatalf("NodeExecutionStarted(%s): %v", nodeID, err)
		}

		state, cmds, err = domain.Advance(state, domain.NodeOutputReceived{
			RunID: runID, NodeID: string(nodeID), ExecID: start.ExecID, SchemaValid: true,
		}, now)
		if err != nil {
			t.Fatalf("NodeOutputReceived(%s): %v", nodeID, err)
		}
		if _, ok := cmds[0].(domain.CmdEvaluateGates); !ok {
			t.Fatalf("expected CmdEvaluateGates for %s, got %v", nodeID, cmds)
		}

		state, cmds, err = domain.Advance(state, domain.NodeGatesEvaluated{
			RunID: runID, NodeID: string(nodeID), ExecID: start.ExecID, Passed: true,
		}, now)
		if err != nil {
			t.Fatalf("NodeGatesEvaluated(%s): %v", nodeID, err)
		}
	}

	if state.Status != domain.RunSucceeded {
		t.Errorf("final Status = %s, want %s", state.Status, domain.RunSucceeded)
	}
}

func TestProjectGraph_ciWatchWaitNodeEntersWaitingDirectly(t *testing.T) {
	def, err := Load("testdata/ci-watch.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	graph, err := ProjectGraph(def)
	if err != nil {
		t.Fatalf("ProjectGraph: %v", err)
	}

	now := time.Unix(0, 0)
	runID := "run_1"
	state, _, err := domain.Advance(domain.RunState{}, domain.TriggerReceived{RunID: runID}, now)
	if err != nil {
		t.Fatalf("TriggerReceived: %v", err)
	}
	state, cmds, err := domain.Advance(state, domain.RunStarted{RunID: runID, Graph: graph}, now)
	if err != nil {
		t.Fatalf("RunStarted: %v", err)
	}
	start := cmds[0].(domain.CmdStartNode)
	state, _, err = domain.Advance(state, domain.NodeExecutionStarted{RunID: runID, NodeID: "implement", ExecID: start.ExecID, Attempt: 1, Iteration: 1}, now)
	if err != nil {
		t.Fatalf("NodeExecutionStarted: %v", err)
	}
	state, _, err = domain.Advance(state, domain.NodeOutputReceived{RunID: runID, NodeID: "implement", ExecID: start.ExecID, SchemaValid: true}, now)
	if err != nil {
		t.Fatalf("NodeOutputReceived: %v", err)
	}
	state, cmds, err = domain.Advance(state, domain.NodeGatesEvaluated{RunID: runID, NodeID: "implement", ExecID: start.ExecID, Passed: true}, now)
	if err != nil {
		t.Fatalf("NodeGatesEvaluated: %v", err)
	}

	var sawEnterWait bool
	for _, c := range cmds {
		if _, ok := c.(domain.CmdEnterWait); ok {
			sawEnterWait = true
		}
	}
	if !sawEnterWait {
		t.Fatalf("expected CmdEnterWait dispatching ci-watch, got %v", cmds)
	}
	exec, ok := state.Executions["ci-watch"]
	if !ok || len(exec) == 0 || exec[len(exec)-1].Status != domain.ExecWaiting {
		t.Fatalf("expected ci-watch to be Waiting immediately after dispatch, got %v", exec)
	}
}
