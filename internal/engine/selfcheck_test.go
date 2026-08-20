package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/executor/local"
)

// TestEngine_selfCheckReportsCleanAfterAHealthyRun is the happy path:
// db integrity clean, no unverifiable executions once a run has actually
// finished.
func TestEngine_selfCheckReportsCleanAfterAHealthyRun(t *testing.T) {
	st := openStore(t)
	workRoot := t.TempDir()
	runID := "run_healthy"

	defPath := filepath.Join(t.TempDir(), "def.yaml")
	if err := os.WriteFile(defPath, []byte(`
name: healthy
nodes:
  - id: n1
    actor: rule
    output: { x: "string" }
`), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}

	eng := newTestEngine(t, st, local.New(local.DefaultBootIDProvider()), local.DefaultBootIDProvider(), workRoot)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := eng.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = eng.Stop(context.Background()) }()

	graph := domain.Graph{
		Entry: "n1",
		Nodes: []domain.Node{{ID: "n1", Retry: domain.RetryPolicy{MaxAttempts: 1}, LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 1}}},
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

	report, err := eng.SelfCheck(ctx)
	if err != nil {
		t.Fatalf("SelfCheck: %v", err)
	}
	if !report.DBClean {
		t.Errorf("DBClean = false, mismatches = %v", report.MismatchedRunIDs)
	}
	if len(report.UnverifiableExecutions) != 0 {
		t.Errorf("UnverifiableExecutions = %v, want none", report.UnverifiableExecutions)
	}
}

// TestEngine_selfCheckRemovesAnOrphanWorkspaceDirectory proves the
// orphan-workspace half is real, not a no-op: a directory under workRoot
// with no owning non-terminal run must be found and removed.
func TestEngine_selfCheckRemovesAnOrphanWorkspaceDirectory(t *testing.T) {
	st := openStore(t)
	workRoot := t.TempDir()

	eng := newTestEngine(t, st, local.New(local.DefaultBootIDProvider()), local.DefaultBootIDProvider(), workRoot)
	ctx := context.Background()
	if _, err := eng.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	orphanDir := filepath.Join(workRoot, "run_ghost")
	if err := os.MkdirAll(orphanDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	report, err := eng.SelfCheck(ctx)
	if err != nil {
		t.Fatalf("SelfCheck: %v", err)
	}
	found := false
	for _, r := range report.OrphanWorkspacesRemoved {
		if r == "run_ghost" {
			found = true
		}
	}
	if !found {
		t.Errorf("OrphanWorkspacesRemoved = %v, want run_ghost", report.OrphanWorkspacesRemoved)
	}
	if _, err := os.Stat(orphanDir); !os.IsNotExist(err) {
		t.Errorf("orphan dir still exists after SelfCheck: err=%v", err)
	}
}
