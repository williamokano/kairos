package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/effect"
	"github.com/williamokano/kairos/internal/policy"
)

// writeTwoEffectDef writes a two-node linear workflow, both actor:
// effect, the second declared to always fail — the multi-effect
// compensation scenario: n1's effect applies, n2's then fails, and the
// run's terminal Failed status must trigger reverse-order (n1-last, so
// n1 is the only one to compensate) compensation.
func writeTwoEffectDef(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "def.yaml")
	yaml := `name: two-effect-node
nodes:
  - id: n1
    actor: effect
    effects: [gh.pr.create]
    with: { branch: kairos/fix, base: main, title: Fix }
  - id: n2
    actor: effect
    effects: [git.push]
    with: { branch: kairos/other }
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}
	return path
}

func twoEffectGraph() domain.Graph {
	return domain.Graph{
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
}

func TestEngine_reverseOrderCompensationOnFailure(t *testing.T) {
	st := openStore(t)
	repo := newNativeResumeTestRepo(t)

	prFake := effect.NewFake("gh.pr.create")
	prFake.AttemptResult = effect.Result{Outcome: effect.Applied, ExternalRef: "acme/backend#418"}
	pushFake := effect.NewFake("git.push")
	pushFake.AttemptResult = effect.Result{Outcome: effect.Failed, Reason: "remote rejected"}

	eng := newTestEngineWithEffects(t, st, testWorkRoot(t), repo,
		map[string]effect.Provider{"gh.pr.create": prFake, "git.push": pushFake},
		policy.Policy{Default: "deny", Effects: map[string]policy.EffectRule{
			"gh.pr.create": {Allow: "*"}, "git.push": {Allow: "*"},
		}}, false, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := eng.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = eng.Stop(context.Background()) }()

	runID := "run_compensate"
	defPath := writeTwoEffectDef(t)
	meta := appendMetaFor(runID)
	if _, err := st.AppendIf(ctx, runID, 0, []domain.Event{
		domain.TriggerReceived{RunID: runID, TriggerRef: "test", DefinitionRef: defPath, CorrelationID: runID},
	}, meta); err != nil {
		t.Fatalf("append trigger: %v", err)
	}
	if _, err := st.AppendIf(ctx, runID, 1, []domain.Event{
		domain.RunStarted{RunID: runID, Graph: twoEffectGraph()},
	}, meta); err != nil {
		t.Fatalf("append run started: %v", err)
	}

	status := waitForTerminal(t, ctx, st, runID, 8*time.Second)
	if status != domain.RunFailed {
		t.Fatalf("status = %q, want %q", status, domain.RunFailed)
	}

	// Compensation runs in a background goroutine (shard.go) — poll for
	// its result instead of assuming it's already done the instant the
	// run reaches Failed.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(prFake.Compensations()) == 0 {
		time.Sleep(20 * time.Millisecond)
	}

	calls := prFake.Compensations()
	if len(calls) != 1 {
		t.Fatalf("gh.pr.create Compensate call count = %d, want 1 (n1's already-applied effect must be compensated)", len(calls))
	}
	if calls[0] != "acme/backend#418" {
		t.Errorf("Compensate called with externalRef %q, want %q", calls[0], "acme/backend#418")
	}
	if len(pushFake.Compensations()) != 0 {
		t.Errorf("git.push Compensate call count = %d, want 0 — n2's effect never applied, nothing to compensate", len(pushFake.Compensations()))
	}

	deadline = time.Now().Add(5 * time.Second)
	found := false
	for time.Now().Before(deadline) && !found {
		envs, err := st.Read(ctx, runID)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		for _, env := range envs {
			if ev, ok := env.Event.(domain.EffectCompensated); ok && ev.NodeID == "n1" {
				found = true
			}
		}
		if !found {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !found {
		t.Error("expected effect.compensated recorded for n1")
	}
}

func TestEngine_compensationLeavesNonCompensableEffectsAppliedWithoutError(t *testing.T) {
	st := openStore(t)
	repo := newNativeResumeTestRepo(t)

	pushFake := effect.NewFake("git.push")
	pushFake.AttemptResult = effect.Result{Outcome: effect.Applied, ExternalRef: "kairos/fix"}
	pushFake.CompensateErr = effect.ErrNotCompensable
	failFake := effect.NewFake("gh.pr.create")
	failFake.AttemptResult = effect.Result{Outcome: effect.Failed, Reason: "boom"}

	eng := newTestEngineWithEffects(t, st, testWorkRoot(t), repo,
		map[string]effect.Provider{"git.push": pushFake, "gh.pr.create": failFake},
		policy.Policy{Default: "deny", Effects: map[string]policy.EffectRule{
			"git.push": {Allow: "*"}, "gh.pr.create": {Allow: "*"},
		}}, false, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := eng.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = eng.Stop(context.Background()) }()

	runID := "run_compensate_noncompensable"
	dir := t.TempDir()
	path := filepath.Join(dir, "def.yaml")
	yaml := `name: two-effect-node
nodes:
  - id: n1
    actor: effect
    effects: [git.push]
    with: { branch: kairos/fix }
  - id: n2
    actor: effect
    effects: [gh.pr.create]
    with: { branch: kairos/fix, base: main, title: Fix }
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}
	meta := appendMetaFor(runID)
	if _, err := st.AppendIf(ctx, runID, 0, []domain.Event{
		domain.TriggerReceived{RunID: runID, TriggerRef: "test", DefinitionRef: path, CorrelationID: runID},
	}, meta); err != nil {
		t.Fatalf("append trigger: %v", err)
	}
	if _, err := st.AppendIf(ctx, runID, 1, []domain.Event{
		domain.RunStarted{RunID: runID, Graph: twoEffectGraph()},
	}, meta); err != nil {
		t.Fatalf("append run started: %v", err)
	}

	status := waitForTerminal(t, ctx, st, runID, 8*time.Second)
	if status != domain.RunFailed {
		t.Fatalf("status = %q, want %q", status, domain.RunFailed)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(pushFake.Compensations()) == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if len(pushFake.Compensations()) != 1 {
		t.Fatalf("Compensate call count = %d, want 1 (attempted even though it errors)", len(pushFake.Compensations()))
	}

	envs, err := st.Read(ctx, runID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, env := range envs {
		if _, ok := env.Event.(domain.EffectCompensated); ok {
			t.Error("expected NO effect.compensated — Compensate returned ErrNotCompensable, so the effect must stay applied, not recorded as reversed")
		}
	}
}
