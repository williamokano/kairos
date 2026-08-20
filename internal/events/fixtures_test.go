package events_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/events"
)

// TestEvents_allHistoricalFixturesProject walks every fixtures/<type>/v<N>.json
// file, validates it against its registered schema, decodes it into the
// concrete domain.Event, and folds it through domain.Advance from a
// scenario-specific seed RunState — asserting it still projects without
// error. Today there is exactly one version per type, so this exercises
// the mechanism rather than a real upcast, but the walk itself is what
// makes a future v2 file purely additive: nothing about this test's shape
// changes when that day comes (AGENTS §4 rule 6).
func TestEvents_allHistoricalFixturesProject(t *testing.T) {
	registry, err := events.Builtin()
	if err != nil {
		t.Fatalf("Builtin: %v", err)
	}

	const root = "fixtures"
	typeDirs, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading fixtures dir: %v", err)
	}

	for _, typeDir := range typeDirs {
		if !typeDir.IsDir() {
			continue
		}
		eventType := typeDir.Name()
		versionFiles, err := os.ReadDir(filepath.Join(root, eventType))
		if err != nil {
			t.Fatalf("reading fixtures for %s: %v", eventType, err)
		}
		for _, vf := range versionFiles {
			version, err := parseVersionFilename(vf.Name())
			if err != nil {
				t.Fatalf("fixture %s/%s: %v", eventType, vf.Name(), err)
			}
			t.Run(eventType+"/"+vf.Name(), func(t *testing.T) {
				payload, err := os.ReadFile(filepath.Join(root, eventType, vf.Name()))
				if err != nil {
					t.Fatalf("reading fixture: %v", err)
				}
				ev, err := registry.Decode(eventType, version, payload)
				if err != nil {
					t.Fatalf("Decode: %v", err)
				}
				if err := projectFixture(eventType, ev); err != nil {
					t.Fatalf("projecting: %v", err)
				}
			})
		}
	}
}

func parseVersionFilename(name string) (int, error) {
	trimmed := strings.TrimSuffix(strings.TrimPrefix(name, "v"), ".json")
	v, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, fmt.Errorf("filename %q is not of the form vN.json: %w", name, err)
	}
	return v, nil
}

const testRunID = "run_01J8QK"

var now = time.Unix(0, 0)

// linearGraph is the seed graph for node "n1": no wait, retry 2, loopguard 3.
func linearGraph() domain.Graph {
	return domain.Graph{
		Entry: "n1",
		Nodes: []domain.Node{{ID: "n1", Retry: domain.RetryPolicy{MaxAttempts: 2}, LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 3}}},
		Edges: map[domain.NodeID]map[domain.EdgeTrigger]domain.NodeID{
			"n1": {domain.OnSuccess: "$succeed", domain.OnFailure: "$fail", domain.OnTimeout: "$fail"},
		},
	}
}

// waitGraph is the seed graph for node "approve": a human wait, entry node.
func waitGraph() domain.Graph {
	return domain.Graph{
		Entry: "approve",
		Nodes: []domain.Node{{
			ID:        "approve",
			Wait:      &domain.WaitSpec{Kind: domain.WaitHuman, OnTimeout: domain.OnTimeoutEscalate},
			Retry:     domain.RetryPolicy{MaxAttempts: 1},
			LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 1},
		}},
		Edges: map[domain.NodeID]map[domain.EdgeTrigger]domain.NodeID{
			"approve": {domain.OnSuccess: "$succeed", domain.OnFailure: "$fail", domain.OnTimeout: "$fail"},
		},
	}
}

func advanceOK(state domain.RunState, ev domain.Event) (domain.RunState, error) {
	next, _, err := domain.Advance(state, ev, now)
	return next, err
}

// seedPending returns a RunState with node "n1" dispatched (Pending).
func seedPending() (domain.RunState, error) {
	state, err := advanceOK(domain.RunState{}, domain.TriggerReceived{RunID: testRunID})
	if err != nil {
		return state, err
	}
	return advanceOK(state, domain.RunStarted{RunID: testRunID, Graph: linearGraph()})
}

// seedExecuting returns a RunState with node "n1" Executing.
func seedExecuting() (domain.RunState, error) {
	state, err := seedPending()
	if err != nil {
		return state, err
	}
	return advanceOK(state, domain.NodeExecutionStarted{RunID: testRunID, NodeID: "n1", ExecID: "n1#a1.i1", Attempt: 1, Iteration: 1})
}

// seedWaiting returns a RunState with node "approve" Waiting.
func seedWaiting() (domain.RunState, error) {
	state, err := advanceOK(domain.RunState{}, domain.TriggerReceived{RunID: testRunID})
	if err != nil {
		return state, err
	}
	return advanceOK(state, domain.RunStarted{RunID: testRunID, Graph: waitGraph()})
}

// projectFixture folds ev through domain.Advance from the scenario each
// event type needs to legally receive it.
func projectFixture(eventType string, ev domain.Event) error {
	switch eventType {
	case "trigger.received":
		_, err := advanceOK(domain.RunState{}, ev)
		return err
	case "run.started":
		state, err := advanceOK(domain.RunState{}, domain.TriggerReceived{RunID: testRunID})
		if err != nil {
			return err
		}
		_, err = advanceOK(state, ev)
		return err
	case "run.rejected":
		state, err := advanceOK(domain.RunState{}, domain.TriggerReceived{RunID: testRunID})
		if err != nil {
			return err
		}
		_, err = advanceOK(state, ev)
		return err
	case "run.cancelled", "run.degraded":
		state, err := seedPending()
		if err != nil {
			return err
		}
		_, err = advanceOK(state, ev)
		return err
	case "run.degraded.resolved":
		state, err := seedPending()
		if err != nil {
			return err
		}
		state, err = advanceOK(state, domain.RunDegraded{RunID: testRunID, Reason: "join saw degrade"})
		if err != nil {
			return err
		}
		_, err = advanceOK(state, ev)
		return err
	case "node.execution.started":
		state, err := seedPending()
		if err != nil {
			return err
		}
		_, err = advanceOK(state, ev)
		return err
	case "node.output.received", "node.execution.failed", "node.execution.interrupted",
		"node.execution.lost", "node.execution.adopted":
		state, err := seedExecuting()
		if err != nil {
			return err
		}
		_, err = advanceOK(state, ev)
		return err
	case "node.gates.evaluated":
		state, err := seedExecuting()
		if err != nil {
			return err
		}
		state, err = advanceOK(state, domain.NodeOutputReceived{RunID: testRunID, NodeID: "n1", ExecID: "n1#a1.i1", SchemaValid: true})
		if err != nil {
			return err
		}
		_, err = advanceOK(state, ev)
		return err
	case "node.wait.resolved", "human.task.created", "human.task.answered":
		state, err := seedWaiting()
		if err != nil {
			return err
		}
		_, err = advanceOK(state, ev)
		return err
	case "engine.started", "engine.stopped", "engine.reconciled", "process.orphan.reaped":
		// System-stream events: not run-scoped, never folded through
		// domain.Advance (see event.go's doc comment on them). "Projects"
		// here just means the decode succeeded — there is no RunState to
		// fold into.
		return nil
	case "llm.session.started", "session.resume.failed", "session.cost.unavailable", "output.repair.attempted",
		"log.degraded", "log.truncated":
		// L08's audit-only facts, plus L09's log-backpressure facts:
		// run-scoped, folded as a no-op (see advance.go's case for them)
		// — same scenario as node.execution.started since they describe
		// the same in-flight exec.
		state, err := seedPending()
		if err != nil {
			return err
		}
		_, err = advanceOK(state, ev)
		return err
	case "constraint.evaluated":
		// L10's per-gate audit fact: folded as a no-op, same scenario as
		// node.gates.evaluated since a gate is evaluated on an Executing
		// exec that has already received (schema-valid) output.
		state, err := seedExecuting()
		if err != nil {
			return err
		}
		state, err = advanceOK(state, domain.NodeOutputReceived{RunID: testRunID, NodeID: "n1", ExecID: "n1#a1.i1", SchemaValid: true})
		if err != nil {
			return err
		}
		_, err = advanceOK(state, ev)
		return err
	case "waiver.grant", "effect.confirmation.requested", "effect.confirmed":
		// L11's audit-only facts: folded as a no-op, same scenario as
		// node.execution.started since they describe the same in-flight
		// exec (a waiver or an effect check both concern a node that has
		// started, before or after its output is known).
		state, err := seedPending()
		if err != nil {
			return err
		}
		_, err = advanceOK(state, ev)
		return err
	default:
		return fmt.Errorf("no fixture scenario registered for event type %q", eventType)
	}
}
