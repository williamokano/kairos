package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/engine"
	"github.com/williamokano/kairos/internal/executor/local"
)

// writeHumanApprovalDef writes a one-node wait: human workflow, optionally
// declaring a wait.weight, and returns its path.
func writeHumanApprovalDef(t *testing.T, weight string) string {
	t.Helper()
	weightLine := ""
	if weight != "" {
		weightLine = "\n      weight: " + weight
	}
	defPath := filepath.Join(t.TempDir(), "def.yaml")
	yaml := `
name: human-approval
nodes:
  - id: approve
    actor: human
    output: { decision: "string!", reason: "string" }
    wait:
      "on": [{ kind: human }]
      timeout: 1h
      onTimeout: park` + weightLine + `
`
	if err := os.WriteFile(defPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}
	return defPath
}

func TestEngine_humanApprovalNodeSuspendsThenAnswers(t *testing.T) {
	st := openStore(t)
	workRoot := t.TempDir()
	runID := "run_human_1"
	defPath := writeHumanApprovalDef(t, "")

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
		Entry: "approve",
		Nodes: []domain.Node{
			{ID: "approve", Wait: &domain.WaitSpec{Kind: domain.WaitHuman, OnTimeout: domain.OnTimeoutPark},
				Retry: domain.RetryPolicy{MaxAttempts: 1}, LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 1}},
		},
		Edges: map[domain.NodeID]map[domain.EdgeTrigger]domain.NodeID{
			"approve": {domain.OnSuccess: domain.SinkSucceed, domain.OnFailure: domain.SinkFail, domain.OnTimeout: domain.SinkFail},
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

	// The node must genuinely suspend: wait for ExecWaiting AND a recorded
	// human.task.created (the latter is appended asynchronously by the
	// live engine loop reacting to RunStarted, not by the direct AppendIf
	// above, so this polls rather than checking once) — and confirm it
	// does NOT proceed on its own.
	deadline := time.Now().Add(3 * time.Second)
	var sawTaskCreated bool
	for time.Now().Before(deadline) && !sawTaskCreated {
		envs, err := st.Read(ctx, runID)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		for _, e := range envs {
			if _, ok := e.Event.(domain.HumanTaskCreated); ok {
				sawTaskCreated = true
			}
		}
		if !sawTaskCreated {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !sawTaskCreated {
		t.Fatal("expected human.task.created to be recorded")
	}
	state, ok, err := st.GetRunState(ctx, runID)
	if err != nil {
		t.Fatalf("GetRunState: %v", err)
	}
	if !ok || state.Executions["approve"][len(state.Executions["approve"])-1].Status != domain.ExecWaiting {
		t.Fatal("node did not reach/stay at ExecWaiting")
	}

	time.Sleep(200 * time.Millisecond)
	state, _, _ = st.GetRunState(ctx, runID)
	if state.Executions["approve"][len(state.Executions["approve"])-1].Status != domain.ExecWaiting {
		t.Fatal("node proceeded without being answered")
	}

	if err := eng.AnswerHumanTask(ctx, runID, "approve", engine.AnswerDecision{Decision: "approve", Reason: "looks good"}); err != nil {
		t.Fatalf("AnswerHumanTask: %v", err)
	}

	deadline = time.Now().Add(3 * time.Second)
	var finalStatus domain.RunStatus
	for time.Now().Before(deadline) {
		state, ok, err := st.GetRunState(ctx, runID)
		if err != nil {
			t.Fatalf("GetRunState: %v", err)
		}
		if ok {
			finalStatus = state.Status
			if finalStatus.Terminal() {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if finalStatus != domain.RunSucceeded {
		t.Fatalf("final status = %s, want succeeded", finalStatus)
	}
}

func TestAnswerHumanTask_reasonRequired(t *testing.T) {
	st := openStore(t)
	eng := newTestEngine(t, st, local.New(local.DefaultBootIDProvider()), local.DefaultBootIDProvider(), t.TempDir())
	ctx := context.Background()
	if _, err := eng.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	err := eng.AnswerHumanTask(ctx, "run_x", "approve", engine.AnswerDecision{Decision: "approve"})
	if err != engine.ErrHumanDecisionReasonRequired {
		t.Fatalf("err = %v, want ErrHumanDecisionReasonRequired", err)
	}
}

func TestEngine_typeWeightRequiresTheTypedNodeID(t *testing.T) {
	st := openStore(t)
	workRoot := t.TempDir()
	runID := "run_human_type"
	defPath := writeHumanApprovalDef(t, "type")

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
		Entry: "approve",
		Nodes: []domain.Node{
			{ID: "approve", Wait: &domain.WaitSpec{Kind: domain.WaitHuman, OnTimeout: domain.OnTimeoutPark},
				Retry: domain.RetryPolicy{MaxAttempts: 1}, LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 1}},
		},
		Edges: map[domain.NodeID]map[domain.EdgeTrigger]domain.NodeID{
			"approve": {domain.OnSuccess: domain.SinkSucceed, domain.OnFailure: domain.SinkFail, domain.OnTimeout: domain.SinkFail},
		},
	}
	meta := appendMetaFor(runID)
	if _, err := st.AppendIf(ctx, runID, 0, []domain.Event{
		domain.TriggerReceived{RunID: runID, TriggerRef: "test", DefinitionRef: defPath, CorrelationID: runID},
	}, meta); err != nil {
		t.Fatalf("append trigger: %v", err)
	}
	if _, err := st.AppendIf(ctx, runID, 1, []domain.Event{domain.RunStarted{RunID: runID, Graph: graph}}, meta); err != nil {
		t.Fatalf("append run started: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		state, ok, _ := st.GetRunState(ctx, runID)
		if ok {
			if execs, ok := state.Executions["approve"]; ok && len(execs) > 0 && execs[len(execs)-1].Status == domain.ExecWaiting {
				goto waiting
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("node never reached ExecWaiting")
waiting:

	if err := eng.AnswerHumanTask(ctx, runID, "approve", engine.AnswerDecision{Decision: "approve", Reason: "ok"}); err != engine.ErrHumanDecisionTypedWordRequired {
		t.Fatalf("missing typed word: err = %v, want ErrHumanDecisionTypedWordRequired", err)
	}
	if err := eng.AnswerHumanTask(ctx, runID, "approve", engine.AnswerDecision{Decision: "approve", Reason: "ok", TypedWord: "not-the-node-id"}); err != engine.ErrHumanDecisionTypedWordRequired {
		t.Fatalf("wrong typed word: err = %v, want ErrHumanDecisionTypedWordRequired", err)
	}
	if err := eng.AnswerHumanTask(ctx, runID, "approve", engine.AnswerDecision{Decision: "approve", Reason: "ok", TypedWord: "approve"}); err != nil {
		t.Fatalf("correct typed word: err = %v, want nil", err)
	}
}
