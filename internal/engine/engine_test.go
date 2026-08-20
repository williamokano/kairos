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

// TestEngine_liveLoopDrivesARuleThenShellWorkflowToSuccess is the
// end-to-end sanity check for the whole advance loop (Store.Subscribe ->
// domain.Advance -> dispatch), using the real internal/executor/local —
// not exectest.Fake — so it also exercises real subprocess dispatch.
func TestEngine_liveLoopDrivesARuleThenShellWorkflowToSuccess(t *testing.T) {
	st := openStore(t)
	workRoot := t.TempDir()
	runID := "run_e2e"

	defPath := filepath.Join(t.TempDir(), "def.yaml")
	yaml := `
name: e2e
nodes:
  - id: n1
    actor: rule
    output: { x: "string" }
  - id: n2
    actor: shell
    prompt: "echo '{\"ok\":true}' > \"$KAIROS_OUTPUT_PATH\""
    output: { ok: "bool!" }
`
	if err := os.WriteFile(defPath, []byte(yaml), 0o600); err != nil {
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
		Nodes: []domain.Node{
			{ID: "n1", Retry: domain.RetryPolicy{MaxAttempts: 1}, LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 1}},
			{ID: "n2", Retry: domain.RetryPolicy{MaxAttempts: 1}, LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 1}},
		},
		Edges: map[domain.NodeID]map[domain.EdgeTrigger]domain.NodeID{
			"n1": {domain.OnSuccess: "n2", domain.OnFailure: domain.SinkFail, domain.OnTimeout: domain.SinkFail},
			"n2": {domain.OnSuccess: domain.SinkSucceed, domain.OnFailure: domain.SinkFail, domain.OnTimeout: domain.SinkFail},
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

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		state, ok, err := st.GetRunState(ctx, runID)
		if err != nil {
			t.Fatalf("GetRunState: %v", err)
		}
		if ok && state.Status.Terminal() {
			if state.Status != domain.RunSucceeded {
				t.Fatalf("run Status = %s, want %s; state=%+v", state.Status, domain.RunSucceeded, state)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("run did not reach a terminal state within the deadline")
}
