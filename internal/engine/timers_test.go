package engine_test

import (
	"context"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/executor/local"
)

// waitGraphWithTimeout builds a one-node Waiting(human) graph whose
// TimeoutAt is fireAt — real enough to exercise armTimer's actual
// time.AfterFunc path, not just handleWaitTimeout's pure fold.
func waitGraphWithTimeout(onTimeout domain.OnTimeoutAction, fireAt time.Time) domain.Graph {
	return domain.Graph{
		Entry: "approve",
		Nodes: []domain.Node{
			{ID: "approve", Wait: &domain.WaitSpec{Kind: domain.WaitHuman, OnTimeout: onTimeout, TimeoutAt: &fireAt},
				Retry: domain.RetryPolicy{MaxAttempts: 1}, LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 1}},
		},
		Edges: map[domain.NodeID]map[domain.EdgeTrigger]domain.NodeID{
			"approve": {domain.OnSuccess: domain.SinkSucceed, domain.OnFailure: domain.SinkFail, domain.OnTimeout: domain.SinkFail},
		},
	}
}

func TestEngine_waitTimeoutParkOnlyMarksOverdue(t *testing.T) {
	st := openStore(t)
	workRoot := t.TempDir()
	runID := "run_timeout_park"
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

	graph := waitGraphWithTimeout(domain.OnTimeoutPark, time.Now().Add(150*time.Millisecond))
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
	var overdue bool
	for time.Now().Before(deadline) {
		state, ok, err := st.GetRunState(ctx, runID)
		if err != nil {
			t.Fatalf("GetRunState: %v", err)
		}
		if ok {
			execs := state.Executions["approve"]
			if len(execs) > 0 && execs[len(execs)-1].Overdue {
				overdue = true
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !overdue {
		t.Fatal("expected the wait to be marked Overdue after its timeout fired")
	}

	state, _, _ := st.GetRunState(ctx, runID)
	execs := state.Executions["approve"]
	if execs[len(execs)-1].Status != domain.ExecWaiting {
		t.Fatalf("status = %s, want still ExecWaiting (onTimeout: park never transitions)", execs[len(execs)-1].Status)
	}
}

func TestEngine_waitTimeoutEscalateParksAndCreatesHumanTask(t *testing.T) {
	st := openStore(t)
	workRoot := t.TempDir()
	runID := "run_timeout_escalate"
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

	graph := waitGraphWithTimeout(domain.OnTimeoutEscalate, time.Now().Add(150*time.Millisecond))
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
	var parked bool
	for time.Now().Before(deadline) {
		state, ok, err := st.GetRunState(ctx, runID)
		if err != nil {
			t.Fatalf("GetRunState: %v", err)
		}
		if ok {
			execs := state.Executions["approve"]
			if len(execs) > 0 && execs[len(execs)-1].Status == domain.ExecParked {
				parked = true
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !parked {
		t.Fatal("expected the node to reach ExecParked after its timeout escalated")
	}

	state, _, _ := st.GetRunState(ctx, runID)
	if reason := state.Executions["approve"][0].ParkReason; reason != domain.ParkWaitTimeoutEscalate {
		t.Fatalf("ParkReason = %q, want %q", reason, domain.ParkWaitTimeoutEscalate)
	}

	// A second human.task.created (this one for the Parked escalation)
	// must also be recorded, not silently dropped.
	envs, err := st.Read(ctx, runID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var taskCount int
	for _, e := range envs {
		if _, ok := e.Event.(domain.HumanTaskCreated); ok {
			taskCount++
		}
	}
	if taskCount < 2 {
		t.Fatalf("human.task.created count = %d, want at least 2 (the original wait + the escalation)", taskCount)
	}
}

// TestEngine_restartCatchesUpAnOverdueWaitTimeout proves the in-memory
// timer's documented persistence gap is bridged by Start's own catch-up
// pass (rearmOutstandingTimers): a timeout that fell due while no daemon
// was running still fires, the first moment a new one boots.
func TestEngine_restartCatchesUpAnOverdueWaitTimeout(t *testing.T) {
	st := openStore(t)
	workRoot := t.TempDir()
	runID := "run_timeout_catchup"
	defPath := writeHumanApprovalDef(t, "")

	ctx := context.Background()
	graph := waitGraphWithTimeout(domain.OnTimeoutEscalate, time.Now().Add(-1*time.Minute)) // already overdue
	meta := appendMetaFor(runID)
	if _, err := st.AppendIf(ctx, runID, 0, []domain.Event{
		domain.TriggerReceived{RunID: runID, TriggerRef: "test", DefinitionRef: defPath, CorrelationID: runID},
	}, meta); err != nil {
		t.Fatalf("append trigger: %v", err)
	}
	if _, err := st.AppendIf(ctx, runID, 1, []domain.Event{domain.RunStarted{RunID: runID, Graph: graph}}, meta); err != nil {
		t.Fatalf("append run started: %v", err)
	}

	// No engine has ever run yet — this is a cold boot against a log that
	// already contains a Waiting(human) node whose timeout is in the past.
	eng := newTestEngine(t, st, local.New(local.DefaultBootIDProvider()), local.DefaultBootIDProvider(), workRoot)
	rctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := eng.Reconcile(rctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := eng.Start(rctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = eng.Stop(context.Background()) }()

	state, ok, err := st.GetRunState(rctx, runID)
	if err != nil {
		t.Fatalf("GetRunState: %v", err)
	}
	if !ok || state.Executions["approve"][0].Status != domain.ExecParked {
		t.Fatalf("expected the overdue timeout to have already resolved to ExecParked by the time Start returned, got %+v", state.Executions["approve"])
	}
}
