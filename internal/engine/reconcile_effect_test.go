package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/effect"
	"github.com/williamokano/kairos/internal/eventstore"
	"github.com/williamokano/kairos/internal/policy"
)

// writeEffectLikeDefinition writes a one-node actor: effect workflow —
// the L12 counterpart to reconcile_test.go's writeMilestoneLikeDefinition.
func writeEffectLikeDefinition(t *testing.T, dir, effectName string) string {
	t.Helper()
	path := filepath.Join(dir, "def.yaml")
	yaml := "name: reconcile-effect-test\nnodes:\n  - id: n1\n    actor: effect\n    effects: [" + effectName + "]\n    with: { branch: kairos/fix, base: main, title: Fix }\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}
	return path
}

// seedExecutingEffectRun writes trigger->started->EffectAttempted (if
// attempted) for a fresh run, leaving the store exactly as a real crash
// mid-effect would — 06-durability.md's "Effect in Attempted with no
// result" scenario, built deterministically rather than by racing a real
// process kill (matching reconcile_test.go's own seedExecutingRun style).
func seedExecutingEffectRun(t *testing.T, st eventstore.Store, runID, definitionRef, effectName string, attempted bool) string {
	t.Helper()
	return seedExecutingEffectRunWithMaxAttempts(t, st, runID, definitionRef, effectName, attempted, 1)
}

// seedExecutingEffectRunWithMaxAttempts is seedExecutingEffectRun with a
// caller-chosen domain.Graph retry policy — the graph's own
// Retry.MaxAttempts (not the YAML definition's retry: block, which
// registry.Load never even runs here since no dispatch reads it before
// this point) is what advanceNodeExecutionLost consults, so a test that
// needs a retry to actually happen must set this to at least 2 (matching
// L05's original milestone gotcha: attempt(1) < MaxAttempts is required
// for a fresh attempt to be allocated instead of routing straight to
// $fail).
func seedExecutingEffectRunWithMaxAttempts(t *testing.T, st eventstore.Store, runID, definitionRef, effectName string, attempted bool, maxAttempts int) string {
	t.Helper()
	ctx := context.Background()
	meta := eventstore.AppendMeta{Actor: "test", CorrelationID: runID, OccurredAt: time.Unix(0, 0)}

	graph := domain.Graph{
		Entry: "n1",
		Nodes: []domain.Node{{ID: "n1", Retry: domain.RetryPolicy{MaxAttempts: maxAttempts}, LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 1}}},
		Edges: map[domain.NodeID]map[domain.EdgeTrigger]domain.NodeID{
			"n1": {domain.OnSuccess: domain.SinkSucceed, domain.OnFailure: domain.SinkFail, domain.OnTimeout: domain.SinkFail},
		},
	}

	if _, err := st.AppendIf(ctx, runID, 0, []domain.Event{
		domain.TriggerReceived{RunID: runID, TriggerRef: "test", DefinitionRef: definitionRef, CorrelationID: runID},
	}, meta); err != nil {
		t.Fatalf("append trigger: %v", err)
	}
	if _, err := st.AppendIf(ctx, runID, 1, []domain.Event{
		domain.RunStarted{RunID: runID, Graph: graph},
	}, meta); err != nil {
		t.Fatalf("append run started: %v", err)
	}
	execID := "n1#a1.i1"
	if _, err := st.AppendIf(ctx, runID, 2, []domain.Event{
		domain.NodeExecutionStarted{RunID: runID, NodeID: "n1", ExecID: execID, Attempt: 1, Iteration: 1},
	}, meta); err != nil {
		t.Fatalf("append node execution started: %v", err)
	}
	if attempted {
		if _, err := st.AppendIf(ctx, runID, 3, []domain.Event{
			domain.EffectAttempted{RunID: runID, NodeID: "n1", ExecID: execID, Effect: effectName, IdempotencyKey: effect.IdempotencyKey(runID, "n1", effectName)},
		}, meta); err != nil {
			t.Fatalf("append effect attempted: %v", err)
		}
	}
	return execID
}

func TestReconcile_effectAttemptedProbesAppliedAndSucceeds(t *testing.T) {
	st := openStore(t)
	repo := newNativeResumeTestRepo(t)
	fake := effect.NewFake("gh.pr.create")
	fake.ProbeResult = effect.Result{Outcome: effect.Applied, ExternalRef: "acme/backend#418"}
	fake.ProbeOK = true

	runID := "run_reconcile_effect_applied"
	defPath := writeEffectLikeDefinition(t, t.TempDir(), "gh.pr.create")
	seedExecutingEffectRun(t, st, runID, defPath, "gh.pr.create", true)

	eng := newTestEngineWithEffects(t, st, testWorkRoot(t), repo,
		map[string]effect.Provider{"gh.pr.create": fake},
		policy.Policy{Default: "deny", Effects: map[string]policy.EffectRule{"gh.pr.create": {Allow: "*"}}},
		false, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := eng.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if len(fake.ProbeCalls) != 1 {
		t.Fatalf("Probe call count = %d, want 1", len(fake.ProbeCalls))
	}
	if len(fake.AttemptCalls) != 0 {
		t.Errorf("Attempt call count = %d, want 0 — a probed-applied effect must never be re-attempted", len(fake.AttemptCalls))
	}

	state, ok, err := st.GetRunState(ctx, runID)
	if err != nil || !ok {
		t.Fatalf("GetRunState: ok=%v err=%v", ok, err)
	}
	if state.Status != domain.RunSucceeded {
		t.Fatalf("status = %q, want %q", state.Status, domain.RunSucceeded)
	}
}

func TestReconcile_effectAttemptedProbesFailedAndFails(t *testing.T) {
	st := openStore(t)
	repo := newNativeResumeTestRepo(t)
	fake := effect.NewFake("gh.pr.create")
	fake.ProbeResult = effect.Result{Outcome: effect.Failed, Reason: "no such PR"}
	fake.ProbeOK = true

	runID := "run_reconcile_effect_failed"
	defPath := writeEffectLikeDefinition(t, t.TempDir(), "gh.pr.create")
	seedExecutingEffectRun(t, st, runID, defPath, "gh.pr.create", true)

	eng := newTestEngineWithEffects(t, st, testWorkRoot(t), repo,
		map[string]effect.Provider{"gh.pr.create": fake},
		policy.Policy{Default: "deny", Effects: map[string]policy.EffectRule{"gh.pr.create": {Allow: "*"}}},
		false, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := eng.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	state, ok, err := st.GetRunState(ctx, runID)
	if err != nil || !ok {
		t.Fatalf("GetRunState: ok=%v err=%v", ok, err)
	}
	if state.Status != domain.RunFailed {
		t.Fatalf("status = %q, want %q", state.Status, domain.RunFailed)
	}
}

func TestReconcile_effectAttemptedUnprobeableRecordsUnknownAndBlocksTerminal(t *testing.T) {
	st := openStore(t)
	repo := newNativeResumeTestRepo(t)
	fake := effect.NewFake("gh.pr.create")
	fake.ProbeOK = false // the provider genuinely cannot say

	runID := "run_reconcile_effect_unknown"
	defPath := writeEffectLikeDefinition(t, t.TempDir(), "gh.pr.create")
	seedExecutingEffectRun(t, st, runID, defPath, "gh.pr.create", true)

	eng := newTestEngineWithEffects(t, st, testWorkRoot(t), repo,
		map[string]effect.Provider{"gh.pr.create": fake},
		policy.Policy{Default: "deny", Effects: map[string]policy.EffectRule{"gh.pr.create": {Allow: "*"}}},
		false, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := eng.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	state, ok, err := st.GetRunState(ctx, runID)
	if err != nil || !ok {
		t.Fatalf("GetRunState: ok=%v err=%v", ok, err)
	}
	if state.Status.Terminal() {
		t.Fatalf("status = %q — an unprobeable effect must block the run from reaching a terminal status", state.Status)
	}
	execs := state.Executions["n1"]
	if len(execs) == 0 || execs[len(execs)-1].Status != domain.ExecExecuting {
		t.Fatalf("expected n1's exec to remain Executing, got %+v", execs)
	}

	envs, err := st.Read(ctx, runID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	found := false
	for _, env := range envs {
		if _, ok := env.Event.(domain.EffectUnknown); ok {
			found = true
		}
	}
	if !found {
		t.Error("expected effect.unknown to be recorded")
	}
}

func TestReconcile_effectNeverAttemptedIsTreatedAsLostAndRetried(t *testing.T) {
	st := openStore(t)
	repo := newNativeResumeTestRepo(t)
	fake := effect.NewFake("gh.pr.create")
	fake.AttemptResult = effect.Result{Outcome: effect.Applied, ExternalRef: "acme/backend#1"}

	runID := "run_reconcile_effect_never_attempted"
	dir := t.TempDir()
	defPath := filepath.Join(dir, "def.yaml")
	// sideEffectFree: true resolves RestartPolicy to "rerun" (defaults.go)
	// rather than the "fail-to-human" default — correct here because
	// nothing external ever happened (no EffectAttempted was recorded),
	// so an automatic retry from scratch is genuinely safe, unlike a
	// crash mid-effect (the other three tests in this file), where
	// retrying blindly is exactly what 06-durability.md's probe-first
	// recovery exists to prevent.
	yaml := "name: reconcile-effect-test\nnodes:\n  - id: n1\n    actor: effect\n    sideEffectFree: true\n    effects: [gh.pr.create]\n    with: { branch: kairos/fix, base: main, title: Fix }\n"
	if err := os.WriteFile(defPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}
	// Crashed before EffectAttempted was ever recorded — nothing was
	// externally attempted, safe to retry from scratch. maxAttempts: 2
	// is required for the same reason L05's milestone needed it on n2:
	// a Lost exec's attempt(1) must be < MaxAttempts for
	// advanceNodeExecutionLost to allocate a fresh attempt rather than
	// routing straight to $fail.
	seedExecutingEffectRunWithMaxAttempts(t, st, runID, defPath, "gh.pr.create", false, 2)

	eng := newTestEngineWithEffects(t, st, testWorkRoot(t), repo,
		map[string]effect.Provider{"gh.pr.create": fake},
		policy.Policy{Default: "deny", Effects: map[string]policy.EffectRule{"gh.pr.create": {Allow: "*"}}},
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

	status := waitForTerminal(t, ctx, st, runID, 8*time.Second)
	if status != domain.RunSucceeded {
		t.Fatalf("status = %q, want %q", status, domain.RunSucceeded)
	}
}
