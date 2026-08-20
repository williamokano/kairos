package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/conversation"
	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/executor/local"
)

// TestEngine_waitConversationSuspendsThenResumesOnMessage is the first
// real exercise of the WaitConversation kind (L14): a node with wait:
// conversation must genuinely suspend (not silently proceed) until a
// message is posted to its run's Conversation, and must genuinely resume
// (not hang forever) once one arrives — proven under a real wall clock
// against the live engine loop, mirroring
// TestEngine_liveLoopDrivesARuleThenShellWorkflowToSuccess's harness.
func TestEngine_waitConversationSuspendsThenResumesOnMessage(t *testing.T) {
	st := openStore(t)
	workRoot := t.TempDir()
	runID := "run_wait_conv"

	defPath := filepath.Join(t.TempDir(), "def.yaml")
	yaml := `
name: wait-conv
nodes:
  - id: n1
    actor: rule
    output: { x: "string" }
  - id: n2
    actor: rule
    wait:
      "on":
        - kind: conversation
      onTimeout: escalate
    output: { message: "string!" }
  - id: n3
    actor: shell
    prompt: "echo '{\"ok\":true}' > \"$KAIROS_OUTPUT_PATH\""
    output: { ok: "bool!" }
`
	if err := os.WriteFile(defPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}

	eng := newTestEngine(t, st, local.New(local.DefaultBootIDProvider()), local.DefaultBootIDProvider(), workRoot)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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
			{
				ID: "n2", Retry: domain.RetryPolicy{MaxAttempts: 1}, LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 1},
				Wait: &domain.WaitSpec{Kind: domain.WaitConversation, OnTimeout: domain.OnTimeoutEscalate},
			},
			{ID: "n3", Retry: domain.RetryPolicy{MaxAttempts: 1}, LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 1}},
		},
		Edges: map[domain.NodeID]map[domain.EdgeTrigger]domain.NodeID{
			"n1": {domain.OnSuccess: "n2", domain.OnFailure: domain.SinkFail, domain.OnTimeout: domain.SinkFail},
			"n2": {domain.OnSuccess: "n3", domain.OnFailure: domain.SinkFail, domain.OnTimeout: domain.SinkFail},
			"n3": {domain.OnSuccess: domain.SinkSucceed, domain.OnFailure: domain.SinkFail, domain.OnTimeout: domain.SinkFail},
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

	// It must genuinely suspend: give it a real window to (wrongly)
	// proceed on its own, and confirm it does not.
	waitDeadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(waitDeadline) {
		state, ok, err := st.GetRunState(ctx, runID)
		if err != nil {
			t.Fatalf("GetRunState: %v", err)
		}
		if ok && state.Status.Terminal() {
			t.Fatalf("run reached terminal state %s before any conversation message was posted — wait: conversation did not suspend", state.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	state, ok, err := st.GetRunState(ctx, runID)
	if err != nil {
		t.Fatalf("GetRunState: %v", err)
	}
	if !ok {
		t.Fatal("run state not found")
	}
	execs, ok := state.Executions["n2"]
	if !ok || len(execs) == 0 || execs[len(execs)-1].Status != domain.ExecWaiting {
		t.Fatalf("n2 status = %+v, want ExecWaiting", execs)
	}

	// Now post the message it's waiting for and confirm it genuinely
	// resumes (does not hang forever).
	if err := conversation.AppendMessage(ctx, st, runID, "human", "go ahead"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
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
	t.Fatal("run did not resume and reach a terminal state within the deadline")
}

// TestEngine_waitConversationReconcileCatchesUpOnBacklog proves the
// second half of resolveConversationWait's contract: a message posted
// while no engine is subscribed (the daemon was down) is not lost —
// Reconcile's catch-up pass must resolve it, not just the live loop.
func TestEngine_waitConversationReconcileCatchesUpOnBacklog(t *testing.T) {
	st := openStore(t)
	workRoot := t.TempDir()
	runID := "run_wait_conv_backlog"

	defPath := filepath.Join(t.TempDir(), "def.yaml")
	yaml := `
name: wait-conv-backlog
nodes:
  - id: n1
    actor: rule
    wait:
      "on":
        - kind: conversation
      onTimeout: escalate
    output: { message: "string!" }
`
	if err := os.WriteFile(defPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}

	ctx := context.Background()
	graph := domain.Graph{
		Entry: "n1",
		Nodes: []domain.Node{
			{
				ID: "n1", Retry: domain.RetryPolicy{MaxAttempts: 1}, LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 1},
				Wait: &domain.WaitSpec{Kind: domain.WaitConversation, OnTimeout: domain.OnTimeoutEscalate},
			},
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

	// Post the message BEFORE any engine ever subscribes — simulating it
	// arriving while the daemon was down.
	if err := conversation.AppendMessage(ctx, st, runID, "human", "already here"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	eng := newTestEngine(t, st, local.New(local.DefaultBootIDProvider()), local.DefaultBootIDProvider(), workRoot)
	rctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := eng.Reconcile(rctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	state, ok, err := st.GetRunState(rctx, runID)
	if err != nil {
		t.Fatalf("GetRunState: %v", err)
	}
	if !ok {
		t.Fatal("run state not found")
	}
	if state.Status != domain.RunSucceeded {
		t.Fatalf("run Status = %s, want %s (Reconcile should have resolved the backlog message); state=%+v", state.Status, domain.RunSucceeded, state)
	}
}
