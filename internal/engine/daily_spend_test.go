package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/admission"
	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/engine"
	"github.com/williamokano/kairos/internal/executor/local"
)

// TestEngine_reconcileRestoresPersistedDailySpend is the engine-level
// integration proof for the admission-durability fix: Reconcile seeds
// the Manager from Store.GetAdmissionSpend (populated by a real granted
// request's EstimatedCostUSD, via admission's OnSpendChange hook), and a
// second Engine over the same store — the shape a real daemon restart
// takes — resumes counting against that persisted total rather than
// starting over at zero.
func TestEngine_reconcileRestoresPersistedDailySpend(t *testing.T) {
	st := openStore(t)
	workRoot := t.TempDir()
	runID := "run_spend_1"

	defPath := filepath.Join(t.TempDir(), "def.yaml")
	yaml := `
name: spend
nodes:
  - id: n1
    actor: rule
    resources: { model: { maxCostUSD: 20 } }
    output: { x: "string" }
`
	if err := os.WriteFile(defPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}

	eng1 := engine.New(engine.Config{
		Store: st, Executor: local.New(local.DefaultBootIDProvider()), BootID: local.DefaultBootIDProvider(),
		WorkRoot: workRoot, Admission: admission.Config{DailyUSD: 25},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := eng1.Reconcile(ctx); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	if err := eng1.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}

	graph := domain.Graph{
		Entry: "n1",
		Nodes: []domain.Node{
			{ID: "n1", Retry: domain.RetryPolicy{MaxAttempts: 1}, LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 1}},
		},
		Edges: map[domain.NodeID]map[domain.EdgeTrigger]domain.NodeID{
			"n1": {domain.OnSuccess: domain.SinkSucceed, domain.OnFailure: domain.SinkFail, domain.OnTimeout: domain.SinkFail},
		},
	}
	meta := appendMetaFor(runID)
	if _, err := st.AppendIf(ctx, runID, 0, []domain.Event{
		domain.TriggerReceived{RunID: runID, TriggerRef: "test", DefinitionRef: defPath, CorrelationID: runID},
	}, meta); err != nil {
		t.Fatalf("append trigger: %v", err)
	}
	if _, err := st.AppendIf(ctx, runID, 1, []domain.Event{
		domain.RunStarted{RunID: runID, Graph: graph},
	}, meta); err != nil {
		t.Fatalf("append run started: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		state, ok, err := st.GetRunState(ctx, runID)
		if err != nil {
			t.Fatalf("GetRunState: %v", err)
		}
		if ok && state.Status.Terminal() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = eng1.Stop(context.Background())

	// A day key computed the same way admission.Manager does (local
	// calendar date) — Reconcile is what actually reads it back, this is
	// only used to assert the persisted row landed.
	today := time.Now().Local().Format("2006-01-02")
	spent, ok, err := st.GetAdmissionSpend(ctx, today)
	if err != nil {
		t.Fatalf("GetAdmissionSpend: %v", err)
	}
	if !ok || spent != 20 {
		t.Fatalf("persisted spend = (%v, %v), want (20, true) — a granted node's EstimatedCostUSD must be persisted via OnSpendChange", spent, ok)
	}

	// A fresh Engine over the SAME store — the shape a real daemon
	// restart takes (new process, same $KAIROS_HOME/kairos.db). Its own
	// Reconcile must seed the Manager from the row above.
	eng2 := engine.New(engine.Config{
		Store: st, Executor: local.New(local.DefaultBootIDProvider()), BootID: local.DefaultBootIDProvider(),
		WorkRoot: workRoot, Admission: admission.Config{DailyUSD: 25},
	})
	if _, err := eng2.Reconcile(ctx); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	// Start (subscribing to the live bus) must happen before appending
	// this run's events — the live loop only delivers events appended
	// after subscription, exactly like eng1's earlier sequencing above.
	if err := eng2.Start(ctx); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	defer func() { _ = eng2.Stop(context.Background()) }()

	runID2 := "run_spend_2"
	if _, err := st.AppendIf(ctx, runID2, 0, []domain.Event{
		domain.TriggerReceived{RunID: runID2, TriggerRef: "test", DefinitionRef: defPath, CorrelationID: runID2},
	}, appendMetaFor(runID2)); err != nil {
		t.Fatalf("append trigger 2: %v", err)
	}
	if _, err := st.AppendIf(ctx, runID2, 1, []domain.Event{
		domain.RunStarted{RunID: runID2, Graph: graph},
	}, appendMetaFor(runID2)); err != nil {
		t.Fatalf("append run started 2: %v", err)
	}

	// $20 already spent today (restored by the second Reconcile's Seed)
	// + this run's own $20 estimate exceeds the $25 cap — only correctly
	// denied if restoration actually happened, not a silent reset to
	// zero that would let both runs' $20 requests through independently.
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		state, ok, err := st.GetRunState(ctx, runID2)
		if err != nil {
			t.Fatalf("GetRunState(run2): %v", err)
		}
		if ok && state.Status.Terminal() {
			if state.Status != domain.RunFailed {
				t.Fatalf("run2 Status = %s, want %s (denied by rule 5 using the restored total) — state=%+v", state.Status, domain.RunFailed, state)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("run2 did not reach a terminal state within the deadline")
}
