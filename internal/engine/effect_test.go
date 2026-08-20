package engine_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/effect"
	"github.com/williamokano/kairos/internal/engine"
	"github.com/williamokano/kairos/internal/eventstore"
	"github.com/williamokano/kairos/internal/executor/local"
	"github.com/williamokano/kairos/internal/policy"
)

// testWorkRoot nests one level below its own dedicated tempdir so the
// engine's default MirrorRoot (filepath.Dir(workRoot)) never lands on a
// t.TempDir() call shared with repo — see native_resume_test.go's
// newNativeResumeTestRepo caller for the full explanation of why a bare
// t.TempDir() here would trip ADR 0005's "inside kairos's own state
// directory" refusal.
func testWorkRoot(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "work")
}

// newTestEngineWithEffects mirrors newTestEngineWithPolicy, adding the
// effect-provider/dry-run/ceiling knobs L12 introduces. repo must be a
// real git repo (newNativeResumeTestRepo) — dispatchEffectActor always
// provisions a real workspace clone before consulting the provider, even
// a Fake one, matching dispatchShellActor's own workspace: write
// handling exactly.
func newTestEngineWithEffects(t *testing.T, st eventstore.Store, workRoot, repo string, providers map[string]effect.Provider, pol policy.Policy, dryRun bool, ceilings map[string]int) *engine.Engine {
	t.Helper()
	return engine.New(engine.Config{
		Store:                    st,
		Executor:                 local.New(local.DefaultBootIDProvider()),
		BootID:                   local.DefaultBootIDProvider(),
		WorkRoot:                 workRoot,
		WorkspaceRepo:            repo,
		KillGrace:                200 * time.Millisecond,
		Policy:                   pol,
		EffectProviders:          providers,
		DryRun:                   dryRun,
		UnattendedEffectCeilings: ceilings,
		Logger:                   slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	})
}

// effectGraph is a one-node "n1" graph whose actor is "effect".
func effectGraph() domain.Graph {
	return domain.Graph{
		Entry: "n1",
		Nodes: []domain.Node{{ID: "n1", Retry: domain.RetryPolicy{MaxAttempts: 1}, LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 1}}},
		Edges: map[domain.NodeID]map[domain.EdgeTrigger]domain.NodeID{
			"n1": {domain.OnSuccess: domain.SinkSucceed, domain.OnFailure: domain.SinkFail, domain.OnTimeout: domain.SinkFail},
		},
	}
}

func writeEffectDef(t *testing.T, effectName string, extra string) string {
	t.Helper()
	defPath := filepath.Join(t.TempDir(), "def.yaml")
	yaml := "name: effect-node\nnodes:\n  - id: n1\n    actor: effect\n    effects: [" + effectName + "]\n" + extra + "\n"
	if err := os.WriteFile(defPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}
	return defPath
}

func startEffectRun(t *testing.T, ctx context.Context, st eventstore.Store, runID, defPath string) {
	t.Helper()
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
}

func TestEngine_effectActorSucceedsAndRecordsTheFullStateMachine(t *testing.T) {
	st := openStore(t)
	repo := newNativeResumeTestRepo(t)
	fake := effect.NewFake("git.push")
	fake.AttemptResult = effect.Result{Outcome: effect.Applied, ExternalRef: "kairos/fix"}

	eng := newTestEngineWithEffects(t, st, testWorkRoot(t), repo,
		map[string]effect.Provider{"git.push": fake},
		policy.Policy{Default: "deny", Effects: map[string]policy.EffectRule{"git.push": {Allow: "*"}}},
		false, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := eng.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = eng.Stop(context.Background()) }()

	runID := "run_effect_success"
	startEffectRun(t, ctx, st, runID, writeEffectDef(t, "git.push", "    with: { branch: kairos/fix }"))

	status := waitForTerminal(t, ctx, st, runID, 8*time.Second)
	if status != domain.RunSucceeded {
		t.Fatalf("status = %q, want %q", status, domain.RunSucceeded)
	}
	if fake.CallCount() != 1 {
		t.Errorf("Attempt call count = %d, want 1", fake.CallCount())
	}

	envs, err := st.Read(ctx, runID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var sawAttempted, sawApplied bool
	for _, env := range envs {
		switch env.Event.(type) {
		case domain.EffectAttempted:
			sawAttempted = true
		case domain.EffectApplied:
			sawApplied = true
		}
	}
	if !sawAttempted || !sawApplied {
		t.Errorf("sawAttempted=%v sawApplied=%v, want both true", sawAttempted, sawApplied)
	}
}

func TestEngine_effectActorFailureIsRecorded(t *testing.T) {
	st := openStore(t)
	repo := newNativeResumeTestRepo(t)
	fake := effect.NewFake("git.push")
	fake.AttemptResult = effect.Result{Outcome: effect.Failed, Reason: "remote rejected"}

	eng := newTestEngineWithEffects(t, st, testWorkRoot(t), repo,
		map[string]effect.Provider{"git.push": fake},
		policy.Policy{Default: "deny", Effects: map[string]policy.EffectRule{"git.push": {Allow: "*"}}},
		false, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := eng.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = eng.Stop(context.Background()) }()

	runID := "run_effect_failure"
	startEffectRun(t, ctx, st, runID, writeEffectDef(t, "git.push", "    with: { branch: kairos/fix }"))

	status := waitForTerminal(t, ctx, st, runID, 8*time.Second)
	if status != domain.RunFailed {
		t.Fatalf("status = %q, want %q", status, domain.RunFailed)
	}
}

func TestEngine_effectConfirmParkThenApproveResumesAndRunsTheEffect(t *testing.T) {
	st := openStore(t)
	repo := newNativeResumeTestRepo(t)
	fake := effect.NewFake("gh.pr.create")
	fake.AttemptResult = effect.Result{Outcome: effect.Applied, ExternalRef: "acme/backend#418"}

	eng := newTestEngineWithEffects(t, st, testWorkRoot(t), repo,
		map[string]effect.Provider{"gh.pr.create": fake},
		policy.Policy{Default: "deny", Effects: map[string]policy.EffectRule{"gh.pr.create": {Confirm: "each"}}},
		false, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := eng.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = eng.Stop(context.Background()) }()

	runID := "run_effect_confirm_resume"
	startEffectRun(t, ctx, st, runID, writeEffectDef(t, "gh.pr.create", "    with: { branch: kairos/fix, base: main, title: Fix }"))

	// Wait for the park.
	deadline := time.Now().Add(8 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("node never parked (ExecWaiting) within the deadline")
		}
		state, ok, err := st.GetRunState(ctx, runID)
		if err != nil {
			t.Fatalf("GetRunState: %v", err)
		}
		if ok {
			execs := state.Executions["n1"]
			if len(execs) > 0 && execs[len(execs)-1].Status == domain.ExecWaiting {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if fake.CallCount() != 0 {
		t.Fatalf("Attempt was called %d times before approval — the whole point of parking is that it wasn't", fake.CallCount())
	}

	if err := eng.Approve(ctx, runID, "n1", engine.AnswerDecision{Decision: "approve", Reason: "looks good"}); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	status := waitForTerminal(t, ctx, st, runID, 8*time.Second)
	if status != domain.RunSucceeded {
		t.Fatalf("status = %q, want %q", status, domain.RunSucceeded)
	}
	if fake.CallCount() != 1 {
		t.Errorf("Attempt call count = %d, want 1", fake.CallCount())
	}
}

func TestEngine_effectConfirmDeclineRoutesToFailure(t *testing.T) {
	st := openStore(t)
	repo := newNativeResumeTestRepo(t)
	fake := effect.NewFake("gh.pr.create")

	eng := newTestEngineWithEffects(t, st, testWorkRoot(t), repo,
		map[string]effect.Provider{"gh.pr.create": fake},
		policy.Policy{Default: "deny", Effects: map[string]policy.EffectRule{"gh.pr.create": {Confirm: "each"}}},
		false, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := eng.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = eng.Stop(context.Background()) }()

	runID := "run_effect_confirm_decline"
	startEffectRun(t, ctx, st, runID, writeEffectDef(t, "gh.pr.create", "    with: { branch: kairos/fix, base: main, title: Fix }"))

	deadline := time.Now().Add(8 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("node never parked within the deadline")
		}
		state, ok, err := st.GetRunState(ctx, runID)
		if err == nil && ok {
			execs := state.Executions["n1"]
			if len(execs) > 0 && execs[len(execs)-1].Status == domain.ExecWaiting {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := eng.Approve(ctx, runID, "n1", engine.AnswerDecision{Decision: "reject", Reason: "not now"}); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	status := waitForTerminal(t, ctx, st, runID, 8*time.Second)
	if status != domain.RunFailed {
		t.Fatalf("status = %q, want %q", status, domain.RunFailed)
	}
	if fake.CallCount() != 0 {
		t.Errorf("Attempt call count = %d, want 0 — a declined confirmation must never reach the provider", fake.CallCount())
	}
}

func TestEngine_effectDryRunNeverCallsTheProvider(t *testing.T) {
	st := openStore(t)
	repo := newNativeResumeTestRepo(t)
	fake := effect.NewFake("git.push")
	fake.AttemptResult = effect.Result{Outcome: effect.Applied, ExternalRef: "kairos/fix"}

	eng := newTestEngineWithEffects(t, st, testWorkRoot(t), repo,
		map[string]effect.Provider{"git.push": fake},
		policy.Policy{Default: "deny", Effects: map[string]policy.EffectRule{"git.push": {Allow: "*"}}},
		true, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := eng.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = eng.Stop(context.Background()) }()

	runID := "run_effect_dryrun"
	startEffectRun(t, ctx, st, runID, writeEffectDef(t, "git.push", "    with: { branch: kairos/fix }"))

	status := waitForTerminal(t, ctx, st, runID, 8*time.Second)
	if status != domain.RunSucceeded {
		t.Fatalf("status = %q, want %q", status, domain.RunSucceeded)
	}
	if fake.CallCount() != 0 {
		t.Errorf("Attempt call count = %d, want 0 in dry-run mode", fake.CallCount())
	}

	envs, err := st.Read(ctx, runID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	found := false
	for _, env := range envs {
		if _, ok := env.Event.(domain.EffectSimulated); ok {
			found = true
		}
	}
	if !found {
		t.Error("expected effect.simulated to be recorded")
	}
}

func TestEngine_unattendedCeilingBlocksPastTheCap(t *testing.T) {
	st := openStore(t)
	repo := newNativeResumeTestRepo(t)
	fake := effect.NewFake("gh.pr.create")
	fake.AttemptResult = effect.Result{Outcome: effect.Applied, ExternalRef: "acme/backend#1"}

	eng := newTestEngineWithEffects(t, st, testWorkRoot(t), repo,
		map[string]effect.Provider{"gh.pr.create": fake},
		policy.Policy{Default: "deny", Effects: map[string]policy.EffectRule{"gh.pr.create": {Allow: "*"}}},
		false, map[string]int{"gh.pr.create": 0})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := eng.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = eng.Stop(context.Background()) }()

	runID := "run_effect_ceiling"
	startEffectRun(t, ctx, st, runID, writeEffectDef(t, "gh.pr.create", "    with: { branch: kairos/fix, base: main, title: Fix }"))

	status := waitForTerminal(t, ctx, st, runID, 8*time.Second)
	if status != domain.RunFailed {
		t.Fatalf("status = %q, want %q — the ceiling (0) must block immediately", status, domain.RunFailed)
	}
	if fake.CallCount() != 0 {
		t.Errorf("Attempt call count = %d, want 0 — the ceiling must block before the provider is ever called", fake.CallCount())
	}
}
