package eventstore_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/events"
	"github.com/williamokano/kairos/internal/eventstore"
)

func openTestStore(t *testing.T) eventstore.Store {
	t.Helper()
	registry, err := events.Builtin()
	if err != nil {
		t.Fatalf("Builtin: %v", err)
	}
	dir := t.TempDir()
	st, err := eventstore.Open(context.Background(), eventstore.Config{
		Path:     filepath.Join(dir, "kairos.db"),
		Registry: registry,
		Projections: []eventstore.Projection{
			eventstore.RunStateProjection{},
			eventstore.RunIndexProjection{},
		},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestStore_appendIfSucceedsAtExpectedSequence(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	envs, err := st.AppendIf(ctx, "run_1", 0, []domain.Event{
		domain.TriggerReceived{RunID: "run_1", TriggerRef: "cli", DefinitionRef: "def", CorrelationID: "c1"},
	}, eventstore.AppendMeta{Actor: "engine", CorrelationID: "c1", OccurredAt: time.Unix(0, 0)})
	if err != nil {
		t.Fatalf("AppendIf: %v", err)
	}
	if len(envs) != 1 {
		t.Fatalf("len(envs) = %d, want 1", len(envs))
	}
	if envs[0].Sequence != 1 {
		t.Errorf("Sequence = %d, want 1", envs[0].Sequence)
	}
	if envs[0].GlobalSeq == 0 {
		t.Error("expected a nonzero GlobalSeq")
	}
}

func TestStore_appendIfFailsOnSequenceConflict(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	meta := eventstore.AppendMeta{Actor: "engine", CorrelationID: "c1", OccurredAt: time.Unix(0, 0)}
	ev := []domain.Event{domain.TriggerReceived{RunID: "run_1", TriggerRef: "cli", DefinitionRef: "def", CorrelationID: "c1"}}

	if _, err := st.AppendIf(ctx, "run_1", 0, ev, meta); err != nil {
		t.Fatalf("first AppendIf: %v", err)
	}
	// Retrying at the same expectedSeq=0 must conflict — sequence is now 1.
	if _, err := st.AppendIf(ctx, "run_1", 0, ev, meta); err == nil {
		t.Fatal("expected a conflict on the second AppendIf at a stale expectedSeq")
	}
}

func TestStore_readReturnsEventsInSequenceOrder(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	meta := eventstore.AppendMeta{Actor: "engine", CorrelationID: "c1", OccurredAt: time.Unix(0, 0)}

	graph := domain.Graph{
		Entry: "n1",
		Nodes: []domain.Node{{ID: "n1", Retry: domain.RetryPolicy{MaxAttempts: 1}, LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 1}}},
		Edges: map[domain.NodeID]map[domain.EdgeTrigger]domain.NodeID{
			"n1": {domain.OnSuccess: "$succeed", domain.OnFailure: "$fail", domain.OnTimeout: "$fail"},
		},
	}
	if _, err := st.AppendIf(ctx, "run_1", 0, []domain.Event{
		domain.TriggerReceived{RunID: "run_1", TriggerRef: "cli", DefinitionRef: "def", CorrelationID: "c1"},
	}, meta); err != nil {
		t.Fatalf("append trigger: %v", err)
	}
	if _, err := st.AppendIf(ctx, "run_1", 1, []domain.Event{
		domain.RunStarted{RunID: "run_1", Graph: graph},
	}, meta); err != nil {
		t.Fatalf("append run started: %v", err)
	}

	envs, err := st.Read(ctx, "run_1")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(envs) != 2 {
		t.Fatalf("len(envs) = %d, want 2", len(envs))
	}
	if envs[0].EventType != "trigger.received" || envs[1].EventType != "run.started" {
		t.Errorf("event types = %s, %s, want trigger.received, run.started", envs[0].EventType, envs[1].EventType)
	}
	if _, ok := envs[1].Event.(domain.RunStarted); !ok {
		t.Errorf("envs[1].Event = %T, want domain.RunStarted", envs[1].Event)
	}
}

func TestStore_appendIfAppliesProjectionsInTheSameTransaction(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	meta := eventstore.AppendMeta{Actor: "engine", CorrelationID: "c1", OccurredAt: time.Unix(0, 0)}

	if _, err := st.AppendIf(ctx, "run_1", 0, []domain.Event{
		domain.TriggerReceived{RunID: "run_1", TriggerRef: "cli", DefinitionRef: "def", CorrelationID: "c1"},
	}, meta); err != nil {
		t.Fatalf("AppendIf: %v", err)
	}

	report, err := st.Verify(ctx)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(report.MismatchedRunIDs) != 0 {
		t.Errorf("MismatchedRunIDs = %v, want none", report.MismatchedRunIDs)
	}
}
