package engine_test

import (
	"context"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/effect"
	"github.com/williamokano/kairos/internal/policy"
)

func TestEngine_effectsListsRecordedActionsAndResolveUnblocksAnUnknown(t *testing.T) {
	st := openStore(t)
	repo := newNativeResumeTestRepo(t)
	runID := "run_effects_list"

	fake := effect.NewFake("gh.pr.create")
	fake.AttemptResult = effect.Result{Outcome: effect.Applied, ExternalRef: "acme/repo#7"}

	eng := newTestEngineWithEffects(t, st, testWorkRoot(t), repo,
		map[string]effect.Provider{"gh.pr.create": fake}, policy.Policy{Default: "allow"}, false, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := eng.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = eng.Stop(context.Background()) }()

	startEffectRun(t, ctx, st, runID, writeEffectDef(t, "gh.pr.create", ""))

	status := waitForTerminal(t, ctx, st, runID, 8*time.Second)
	if status != domain.RunSucceeded {
		t.Fatalf("status = %q, want %q", status, domain.RunSucceeded)
	}

	summaries, err := eng.Effects(ctx, runID)
	if err != nil {
		t.Fatalf("Effects: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("got %d summaries, want 1: %+v", len(summaries), summaries)
	}
	s := summaries[0]
	if s.Effect != "gh.pr.create" || s.Outcome != "applied" || s.ExternalRef != "acme/repo#7" {
		t.Fatalf("summary = %+v, want applied gh.pr.create acme/repo#7", s)
	}
	if s.Compensated || !s.WouldCompensateOnCancel {
		t.Fatalf("summary = %+v, want WouldCompensateOnCancel=true, Compensated=false", s)
	}
}

// TestEngine_resolveEffectUnknownAppliedUnblocksTheRun seeds exactly the
// event sequence reconcileEffectNode's own Probe-returns-false path
// produces (NodeExecutionStarted, EffectAttempted, EffectUnknown, and
// nothing else — a NodeExecution stuck at Executing with no
// NodeOutputReceived/NodeExecutionFailed fold, 05-gates.md's "blocks the
// run reaching Failed") directly via AppendIf, rather than forcing a real
// crash mid-Attempt, to keep the test deterministic. This engine never
// calls Start, so ResolveEffectUnknown's not-live path
// (appendAndFoldBeforeStart) is what's under test here — see the
// companion cmd/kairos end-to-end test for the live-engine path via a
// real daemon and the real kairos effects/kairos effects resolve verbs.
func TestEngine_resolveEffectUnknownAppliedUnblocksTheRun(t *testing.T) {
	st := openStore(t)
	repo := newNativeResumeTestRepo(t)
	runID := "run_effects_resolve_applied"

	eng := newTestEngineWithEffects(t, st, testWorkRoot(t), repo,
		map[string]effect.Provider{"gh.pr.create": effect.NewFake("gh.pr.create")}, policy.Policy{Default: "allow"}, false, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := eng.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	defPath := writeEffectDef(t, "gh.pr.create", "")
	meta := appendMetaFor(runID)
	if _, err := st.AppendIf(ctx, runID, 0, []domain.Event{
		domain.TriggerReceived{RunID: runID, TriggerRef: "test", DefinitionRef: defPath, CorrelationID: runID},
	}, meta); err != nil {
		t.Fatalf("append trigger: %v", err)
	}
	if _, err := st.AppendIf(ctx, runID, 1, []domain.Event{
		domain.RunStarted{RunID: runID, Graph: effectGraph()},
	}, meta); err != nil {
		t.Fatalf("append run started: %v", err)
	}
	if _, err := st.AppendIf(ctx, runID, 2, []domain.Event{
		domain.NodeExecutionStarted{RunID: runID, NodeID: "n1", ExecID: "n1#a1.i1", Attempt: 1, Iteration: 1},
	}, meta); err != nil {
		t.Fatalf("append node started: %v", err)
	}
	if _, err := st.AppendIf(ctx, runID, 3, []domain.Event{
		domain.EffectAttempted{RunID: runID, NodeID: "n1", ExecID: "n1#a1.i1", Effect: "gh.pr.create", IdempotencyKey: "k1"},
	}, meta); err != nil {
		t.Fatalf("append effect attempted: %v", err)
	}
	if _, err := st.AppendIf(ctx, runID, 4, []domain.Event{
		domain.EffectUnknown{RunID: runID, NodeID: "n1", ExecID: "n1#a1.i1", Effect: "gh.pr.create"},
	}, meta); err != nil {
		t.Fatalf("append effect unknown: %v", err)
	}

	// Confirm the run is genuinely stuck before resolving — the
	// assertion that the mess exists, matching every prior document's
	// discipline for a recovery test.
	state, ok, err := st.GetRunState(ctx, runID)
	if err != nil || !ok {
		t.Fatalf("GetRunState before resolve: ok=%v err=%v", ok, err)
	}
	if state.Status.Terminal() {
		t.Fatalf("run already terminal before resolving — the test seeded the wrong state: %+v", state)
	}

	summaries, err := eng.Effects(ctx, runID)
	if err != nil {
		t.Fatalf("Effects (pre-resolve): %v", err)
	}
	if len(summaries) != 1 || summaries[0].Outcome != "unknown" {
		t.Fatalf("pre-resolve summaries = %+v, want one unknown", summaries)
	}

	if err := eng.ResolveEffectUnknown(ctx, runID, "n1", "applied", "confirmed manually via GitHub UI"); err != nil {
		t.Fatalf("ResolveEffectUnknown: %v", err)
	}

	state, ok, err = st.GetRunState(ctx, runID)
	if err != nil || !ok {
		t.Fatalf("GetRunState after resolve: ok=%v err=%v", ok, err)
	}
	if state.Status != domain.RunSucceeded {
		t.Fatalf("run Status = %s, want %s after manual resolution: %+v", state.Status, domain.RunSucceeded, state)
	}

	summaries, err = eng.Effects(ctx, runID)
	if err != nil {
		t.Fatalf("Effects (post-resolve): %v", err)
	}
	if len(summaries) != 1 || summaries[0].Outcome != "applied" {
		t.Fatalf("post-resolve summaries = %+v, want one applied", summaries)
	}
}

func TestEngine_resolveEffectUnknownRejectsAnInvalidOutcome(t *testing.T) {
	st := openStore(t)
	eng := newTestEngine(t, st, nil, nil, testWorkRoot(t))
	ctx := context.Background()
	err := eng.ResolveEffectUnknown(ctx, "run_missing", "n1", "sideways", "reason")
	if err == nil {
		t.Fatal("want an error for an unknown outcome value, got nil")
	}
}
