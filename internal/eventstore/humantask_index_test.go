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

// openTestStoreWithHumanTasks registers ONLY HumanTaskIndexProjection —
// unlike RunIndexProjection, it never reads run_state_projection and
// switches on raw event types directly, so it needs no accompanying
// RunStateProjection (which would otherwise require every appended event
// to form a fully legal domain.Advance sequence, real event-sourcing
// machinery this test has no need to construct just to prove the index).
func openTestStoreWithHumanTasks(t *testing.T) eventstore.Store {
	t.Helper()
	registry, err := events.Builtin()
	if err != nil {
		t.Fatalf("Builtin: %v", err)
	}
	st, err := eventstore.Open(context.Background(), eventstore.Config{
		Path:     filepath.Join(t.TempDir(), "kairos.db"),
		Registry: registry,
		Projections: []eventstore.Projection{
			eventstore.HumanTaskIndexProjection{},
		},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestHumanTaskIndex_tracksOpenAndClosedTasksForBothKinds is
// L20-webui.md's Documented decision #5's enforcing test:
// HumanTaskIndexProjection must index BOTH a wait: human task
// (HumanTaskCreated/Answered) and a parked confirm-tier effect
// (EffectConfirmationParked/Answered) identically — both resolve
// through the same kairos approve verb — and must remove a row once its
// matching Answered event lands, not just add rows forever.
func TestHumanTaskIndex_tracksOpenAndClosedTasksForBothKinds(t *testing.T) {
	st := openTestStoreWithHumanTasks(t)
	ctx := context.Background()
	meta := eventstore.AppendMeta{Actor: "test", CorrelationID: "c1", OccurredAt: time.Now()}

	if _, err := st.AppendIf(ctx, "run_a", 0, []domain.Event{
		domain.HumanTaskCreated{RunID: "run_a", NodeID: "approve", ExecID: "approve#a1.i1"},
	}, meta); err != nil {
		t.Fatalf("append HumanTaskCreated: %v", err)
	}
	if _, err := st.AppendIf(ctx, "run_b", 0, []domain.Event{
		domain.EffectConfirmationParked{RunID: "run_b", NodeID: "push", ExecID: "push#a1.i1", Effect: "git.push"},
	}, meta); err != nil {
		t.Fatalf("append EffectConfirmationParked: %v", err)
	}

	open, err := st.ListOpenHumanTasks(ctx)
	if err != nil {
		t.Fatalf("ListOpenHumanTasks: %v", err)
	}
	if len(open) != 2 {
		t.Fatalf("got %d open tasks, want 2: %+v", len(open), open)
	}
	byNode := map[string]eventstore.OpenHumanTask{}
	for _, o := range open {
		byNode[o.RunID+"/"+o.NodeID] = o
	}
	if byNode["run_a/approve"].Kind != "human" {
		t.Errorf("run_a/approve kind = %q, want \"human\"", byNode["run_a/approve"].Kind)
	}
	if byNode["run_b/push"].Kind != "effect_confirm" {
		t.Errorf("run_b/push kind = %q, want \"effect_confirm\"", byNode["run_b/push"].Kind)
	}

	// Answering run_a's task must close ONLY that row.
	if _, err := st.AppendIf(ctx, "run_a", 1, []domain.Event{
		domain.HumanTaskAnswered{RunID: "run_a", NodeID: "approve", ExecID: "approve#a1.i1", Output: []byte(`{}`), SchemaValid: true},
	}, meta); err != nil {
		t.Fatalf("append HumanTaskAnswered: %v", err)
	}

	open, err = st.ListOpenHumanTasks(ctx)
	if err != nil {
		t.Fatalf("ListOpenHumanTasks (after answer): %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("got %d open tasks after answering run_a, want 1: %+v", len(open), open)
	}
	if open[0].RunID != "run_b" || open[0].NodeID != "push" {
		t.Errorf("remaining open task = %+v, want run_b/push", open[0])
	}

	// Answering run_b's effect confirmation must close the last one.
	if _, err := st.AppendIf(ctx, "run_b", 1, []domain.Event{
		domain.EffectConfirmationAnswered{RunID: "run_b", NodeID: "push", ExecID: "push#a1.i1", Approved: true, Reason: "looks right"},
	}, meta); err != nil {
		t.Fatalf("append EffectConfirmationAnswered: %v", err)
	}
	open, err = st.ListOpenHumanTasks(ctx)
	if err != nil {
		t.Fatalf("ListOpenHumanTasks (after both answered): %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("got %d open tasks after answering both, want 0: %+v", len(open), open)
	}
}
