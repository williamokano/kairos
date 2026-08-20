package eventstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/events"
	"github.com/williamokano/kairos/internal/eventstore"
	"github.com/williamokano/kairos/internal/store/sqlite"
)

// linearSucceedGraph is a minimal one-node workflow whose sole node
// always routes to $succeed — enough to drive a run to a real terminal
// state without needing internal/engine's dispatch loop (this package
// tests the event store, not the engine).
func linearSucceedGraph() domain.Graph {
	return domain.Graph{
		Entry: "n1",
		Nodes: []domain.Node{{ID: "n1", Retry: domain.RetryPolicy{MaxAttempts: 1}, LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 1}}},
		Edges: map[domain.NodeID]map[domain.EdgeTrigger]domain.NodeID{
			"n1": {domain.OnSuccess: domain.SinkSucceed, domain.OnFailure: domain.SinkFail, domain.OnTimeout: domain.SinkFail},
		},
	}
}

// seedSucceededRun drives runID through TriggerReceived -> RunStarted ->
// NodeExecutionStarted -> NodeOutputReceived, landing it at RunSucceeded
// — a real, complete run history, not a synthetic fragment.
func seedSucceededRun(t *testing.T, st eventstore.Store, runID string) {
	t.Helper()
	ctx := context.Background()
	meta := eventstore.AppendMeta{Actor: "test", CorrelationID: runID, OccurredAt: time.Unix(0, 0)}

	if _, err := st.AppendIf(ctx, runID, 0, []domain.Event{
		domain.TriggerReceived{RunID: runID, TriggerRef: "test", DefinitionRef: "def.yaml", CorrelationID: runID},
	}, meta); err != nil {
		t.Fatalf("append trigger: %v", err)
	}
	if _, err := st.AppendIf(ctx, runID, 1, []domain.Event{
		domain.RunStarted{RunID: runID, Graph: linearSucceedGraph()},
	}, meta); err != nil {
		t.Fatalf("append run started: %v", err)
	}
	if _, err := st.AppendIf(ctx, runID, 2, []domain.Event{
		domain.NodeExecutionStarted{RunID: runID, NodeID: "n1", ExecID: "n1#a1.i1", Attempt: 1, Iteration: 1},
	}, meta); err != nil {
		t.Fatalf("append node started: %v", err)
	}
	if _, err := st.AppendIf(ctx, runID, 3, []domain.Event{
		domain.NodeOutputReceived{RunID: runID, NodeID: "n1", ExecID: "n1#a1.i1", SchemaValid: true},
	}, meta); err != nil {
		t.Fatalf("append node output: %v", err)
	}
}

// TestReplay_matchesProjection is one of AGENTS.md §9's five original
// product-central-claim tests, named since the very start of this
// project (L00) and made real here: replay a corpus of complete runs
// from scratch through domain.Advance and confirm the fold matches what
// RunStateProjection persisted — "replay folds to the same state, or
// durability is a word." Store.Verify (L02/L05/L06) is exactly this
// replay-and-diff, so this test's job is to exercise it against a real,
// multi-run corpus and assert it comes back clean.
func TestReplay_matchesProjection(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	for _, runID := range []string{"run_a", "run_b", "run_c"} {
		seedSucceededRun(t, st, runID)
	}

	report, err := st.Verify(ctx)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(report.MismatchedRunIDs) != 0 {
		t.Errorf("Verify found mismatches on a clean corpus: %v", report.MismatchedRunIDs)
	}
}

// TestReplay_matchesProjection_catchesADeliberateImpurity is the
// "keep the deliberately-injected-impurity test that proves it has
// teeth" companion 06-durability.md names explicitly: without this,
// "the domain is deterministic" is a comment, not a fact. Corrupts
// run_state_projection's persisted status directly (bypassing the store
// entirely, simulating exactly the kind of divergence replay exists to
// catch — a cosmic ray, a hand-edited row, a projection bug) and
// confirms Verify reports it rather than silently agreeing.
func TestReplay_matchesProjection_catchesADeliberateImpurity(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/kairos.db"

	registry, err := events.Builtin()
	if err != nil {
		t.Fatalf("Builtin: %v", err)
	}
	ctx := context.Background()
	st, err := eventstore.Open(ctx, eventstore.Config{
		Path:     dbPath,
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

	seedSucceededRun(t, st, "run_corrupt")
	seedSucceededRun(t, st, "run_clean")

	// Directly corrupt the persisted projection for run_corrupt — the
	// event log itself is untouched, so a correct replay must disagree
	// with what's now sitting in run_state_projection.
	writer, err := sqlite.Open(dbPath, sqlite.ModeWriter)
	if err != nil {
		t.Fatalf("opening writer connection: %v", err)
	}
	if _, err := writer.ExecContext(ctx, `UPDATE run_state_projection SET status = 'failed' WHERE run_id = 'run_corrupt'`); err != nil {
		_ = writer.Close()
		t.Fatalf("corrupting projection: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("closing writer connection: %v", err)
	}

	report, err := st.Verify(ctx)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(report.MismatchedRunIDs) != 1 || report.MismatchedRunIDs[0] != "run_corrupt" {
		t.Fatalf("MismatchedRunIDs = %v, want exactly [run_corrupt]", report.MismatchedRunIDs)
	}
}
