package engine_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/engine"
	"github.com/williamokano/kairos/internal/eventstore"
	"github.com/williamokano/kairos/internal/executor/local"
	"github.com/williamokano/kairos/internal/policy"
)

func newTestEngineWithPolicy(t *testing.T, st eventstore.Store, workRoot string, pol policy.Policy) *engine.Engine {
	t.Helper()
	return engine.New(engine.Config{
		Store:     st,
		Executor:  local.New(local.DefaultBootIDProvider()),
		BootID:    local.DefaultBootIDProvider(),
		WorkRoot:  workRoot,
		KillGrace: 200 * time.Millisecond,
		Policy:    pol,
		Logger:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	})
}

func runEffectScenario(t *testing.T, effect string, pol policy.Policy) (finalStatus domain.RunStatus, failReason domain.FailReason, message string) {
	t.Helper()
	st := openStore(t)
	workRoot := t.TempDir()
	runID := "run_effect_" + effect

	defPath := filepath.Join(t.TempDir(), "def.yaml")
	yaml := "name: effect-node\nnodes:\n  - id: n1\n    actor: shell\n    prompt: \"echo '{\\\"ok\\\":true}' > \\\"$KAIROS_OUTPUT_PATH\\\"\"\n    output: { ok: \"bool!\" }\n    effects: [" + effect + "]\n"
	if err := os.WriteFile(defPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}

	eng := newTestEngineWithPolicy(t, st, workRoot, pol)
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

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		state, ok, err := st.GetRunState(ctx, runID)
		if err != nil {
			t.Fatalf("GetRunState: %v", err)
		}
		if ok && state.Status.Terminal() {
			execs := state.Executions["n1"]
			var reason domain.FailReason
			var msg string
			if len(execs) > 0 {
				reason = execs[len(execs)-1].Reason
			}
			envs, _ := st.Read(ctx, runID)
			for _, env := range envs {
				if f, ok := env.Event.(domain.NodeExecutionFailed); ok {
					msg = f.Message
				}
			}
			return state.Status, reason, msg
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("run did not reach a terminal state within the deadline")
	return "", "", ""
}

func TestEngine_allowTierEffectProceedsWithoutFriction(t *testing.T) {
	pol := policy.Policy{Default: "deny", Effects: map[string]policy.EffectRule{
		"git.commit": {Allow: "*"},
	}}
	status, _, _ := runEffectScenario(t, "git.commit", pol)
	if status != domain.RunSucceeded {
		t.Fatalf("status = %q, want %q", status, domain.RunSucceeded)
	}
}

func TestEngine_denyTierEffectFailsTheNodeWithAReason(t *testing.T) {
	pol := policy.Policy{Default: "deny", Effects: map[string]policy.EffectRule{
		"gh.pr.merge": {Deny: "*", Reason: "Agents propose; humans dispose."},
	}}
	status, reason, msg := runEffectScenario(t, "gh.pr.merge", pol)
	if status != domain.RunFailed {
		t.Fatalf("status = %q, want %q", status, domain.RunFailed)
	}
	if reason != domain.FailPolicyDenied {
		t.Fatalf("Reason = %q, want %q", reason, domain.FailPolicyDenied)
	}
	if msg == "" {
		t.Fatal("expected a non-empty denial message naming the effect and reason")
	}
}

func TestEngine_confirmTierEffectBlocksWithoutARecordedConfirmation(t *testing.T) {
	pol := policy.Policy{Default: "deny", Effects: map[string]policy.EffectRule{
		"gh.pr.create": {Confirm: "each"},
	}}
	status, reason, _ := runEffectScenario(t, "gh.pr.create", pol)
	if status != domain.RunFailed {
		t.Fatalf("status = %q, want %q — no EffectConfirmed was ever recorded", status, domain.RunFailed)
	}
	if reason != domain.FailPolicyDenied {
		t.Fatalf("Reason = %q, want %q", reason, domain.FailPolicyDenied)
	}
}

func TestEngine_effectConfirmationRequestedIsRecordedForAudit(t *testing.T) {
	st := openStore(t)
	workRoot := t.TempDir()
	runID := "run_confirm_audit"
	pol := policy.Policy{Default: "deny", Effects: map[string]policy.EffectRule{"gh.pr.create": {Confirm: "each"}}}

	defPath := filepath.Join(t.TempDir(), "def.yaml")
	yaml := "name: effect-node\nnodes:\n  - id: n1\n    actor: shell\n    prompt: \"echo '{\\\"ok\\\":true}' > \\\"$KAIROS_OUTPUT_PATH\\\"\"\n    output: { ok: \"bool!\" }\n    effects: [gh.pr.create]\n"
	if err := os.WriteFile(defPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}
	eng := newTestEngineWithPolicy(t, st, workRoot, pol)
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
	if _, err := st.AppendIf(ctx, runID, 1, []domain.Event{domain.RunStarted{RunID: runID, Graph: graph}}, meta); err != nil {
		t.Fatalf("append run started: %v", err)
	}

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		envs, err := st.Read(ctx, runID)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		for _, env := range envs {
			if r, ok := env.Event.(domain.EffectConfirmationRequested); ok {
				if r.Effect != "gh.pr.create" {
					t.Fatalf("Effect = %q, want %q", r.Effect, "gh.pr.create")
				}
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("effect.confirmation.requested was never recorded")
}

func TestEngine_confirmTierEffectProceedsOnceConfirmed(t *testing.T) {
	st := openStore(t)
	workRoot := t.TempDir()
	runID := "run_confirmed"
	pol := policy.Policy{Default: "deny", Effects: map[string]policy.EffectRule{"gh.pr.create": {Confirm: "each"}}}

	defPath := filepath.Join(t.TempDir(), "def.yaml")
	yaml := "name: effect-node\nnodes:\n  - id: n1\n    actor: shell\n    prompt: \"echo '{\\\"ok\\\":true}' > \\\"$KAIROS_OUTPUT_PATH\\\"\"\n    output: { ok: \"bool!\" }\n    effects: [gh.pr.create]\n"
	if err := os.WriteFile(defPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}
	eng := newTestEngineWithPolicy(t, st, workRoot, pol)
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

	// Confirm BEFORE RunStarted lands, so checkEffects can never race
	// n1's own dispatch against this grant — GrantEffectConfirmation
	// appends via its own CAS-retry appendNext, landing at seq 1.
	if err := eng.GrantEffectConfirmation(ctx, runID, "n1", "gh.pr.create", "once"); err != nil {
		t.Fatalf("GrantEffectConfirmation: %v", err)
	}

	envs, err := st.Read(ctx, runID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if _, err := st.AppendIf(ctx, runID, len(envs), []domain.Event{domain.RunStarted{RunID: runID, Graph: graph}}, meta); err != nil {
		t.Fatalf("append run started: %v", err)
	}

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		state, ok, err := st.GetRunState(ctx, runID)
		if err != nil {
			t.Fatalf("GetRunState: %v", err)
		}
		if ok && state.Status == domain.RunSucceeded {
			return
		}
		if ok && state.Status == domain.RunFailed {
			t.Fatal("run failed despite GrantEffectConfirmation having been recorded")
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("run did not reach RunSucceeded within the deadline")
}
