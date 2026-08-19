package eventstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/eventstore"
)

func TestStore_listRunsReflectsAppendedEvents(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	meta := eventstore.AppendMeta{Actor: "engine", CorrelationID: "c1", OccurredAt: time.Unix(0, 0)}

	if _, err := st.AppendIf(ctx, "run_1", 0, []domain.Event{
		domain.TriggerReceived{RunID: "run_1", TriggerRef: "cli", DefinitionRef: "def", CorrelationID: "c1"},
	}, meta); err != nil {
		t.Fatalf("AppendIf: %v", err)
	}

	runs, err := st.ListRuns(ctx, nil)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].RunID != "run_1" {
		t.Fatalf("ListRuns = %+v, want one run_1", runs)
	}
	if runs[0].Status != domain.RunPending {
		t.Errorf("Status = %s, want %s", runs[0].Status, domain.RunPending)
	}

	pending := domain.RunPending
	filtered, err := st.ListRuns(ctx, &pending)
	if err != nil {
		t.Fatalf("ListRuns(pending): %v", err)
	}
	if len(filtered) != 1 {
		t.Errorf("filtered ListRuns = %+v, want one match", filtered)
	}

	running := domain.RunRunning
	none, err := st.ListRuns(ctx, &running)
	if err != nil {
		t.Fatalf("ListRuns(running): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("ListRuns(running) = %+v, want none", none)
	}
}

func TestStore_getRunStateReturnsFalseForUnknownRun(t *testing.T) {
	st := openTestStore(t)
	_, ok, err := st.GetRunState(context.Background(), "no-such-run")
	if err != nil {
		t.Fatalf("GetRunState: %v", err)
	}
	if ok {
		t.Error("expected ok=false for an unknown run")
	}
}

func TestStore_getRunStateReflectsFoldedState(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	meta := eventstore.AppendMeta{Actor: "engine", CorrelationID: "c1", OccurredAt: time.Unix(0, 0)}

	if _, err := st.AppendIf(ctx, "run_1", 0, []domain.Event{
		domain.TriggerReceived{RunID: "run_1", TriggerRef: "cli", DefinitionRef: "def", CorrelationID: "c1"},
	}, meta); err != nil {
		t.Fatalf("AppendIf: %v", err)
	}

	state, ok, err := st.GetRunState(ctx, "run_1")
	if err != nil {
		t.Fatalf("GetRunState: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if state.Status != domain.RunPending {
		t.Errorf("Status = %s, want %s", state.Status, domain.RunPending)
	}
}
