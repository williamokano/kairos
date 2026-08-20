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

const waivableGateDef = `
name: waivable-true
limits:
  loopGuard: { maxIterationsPerNode: 1, onExceeded: escalate-to-human }
gates:
  guardrails:
    kind: command
    waivable: true
    check:
      command: ["false"]
      expect: { exitCode: 0 }
nodes:
  - id: n1
    actor: shell
    prompt: "echo '{\"ok\":true}' > \"$KAIROS_OUTPUT_PATH\""
    output: { ok: "bool!" }
    gates: [guardrails]
`

// TestEngine_waivableTrueGateFailureCanBeWaived proves the positive half
// of 05-gates.md's waiver mechanism: a human-authored WaiverGranted for a
// waivable: true gate's failure unblocks the run, while the raw
// constraint.evaluated fact still records the real failure — the waiver
// changes routing, never the evidence.
func TestEngine_waivableTrueGateFailureCanBeWaived(t *testing.T) {
	st := openStore(t)
	workRoot := t.TempDir()
	runID := "run_waivable_true"

	defPath := filepath.Join(t.TempDir(), "def.yaml")
	if err := os.WriteFile(defPath, []byte(waivableGateDef), 0o600); err != nil {
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

	// Grant the waiver right after the run starts, before n1 has even
	// been dispatched — waiverActive is looked up fresh on every gate
	// evaluation (GrantWaiver appends via the engine's own CAS-retry
	// appendNext, so it is safe to interleave with the shard goroutine
	// already processing this stream), and 05-gates.md places no
	// ordering requirement on when a human answers relative to the
	// node's own execution.
	if err := eng.GrantWaiver(ctx, "human", runID, "n1", "guardrails", "known flaky check, tracked", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("GrantWaiver: %v", err)
	}

	deadline := time.Now().Add(8 * time.Second)
	var sawConstraintFailure bool
	for time.Now().Before(deadline) {
		envs, err := st.Read(ctx, runID)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		for _, env := range envs {
			if ce, ok := env.Event.(domain.ConstraintEvaluated); ok && ce.GateID == "guardrails" && !ce.Passed {
				sawConstraintFailure = true
			}
		}
		state, ok, err := st.GetRunState(ctx, runID)
		if err != nil {
			t.Fatalf("GetRunState: %v", err)
		}
		if ok && state.Status == domain.RunSucceeded {
			if !sawConstraintFailure {
				t.Fatal("run succeeded but constraint.evaluated never recorded the gate's real (failing) outcome — the waiver must not fake the evidence")
			}
			return
		}
		if ok && state.Status == domain.RunFailed {
			t.Fatal("run failed despite a valid waiver for its only failing, waivable gate")
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("run did not reach RunSucceeded within the deadline")
}

// TestEngine_grantWaiverRejectsNonHumanActor proves 05-gates.md's
// "waiver.grant is deny-tier for every non-human principal" as a real,
// tested invariant, not merely an absent code path: GrantWaiver itself
// refuses any actor other than exactly "human".
func TestEngine_grantWaiverRejectsNonHumanActor(t *testing.T) {
	st := openStore(t)
	workRoot := t.TempDir()
	eng := newTestEngine(t, st, local.New(local.DefaultBootIDProvider()), local.DefaultBootIDProvider(), workRoot)

	for _, actor := range []string{"agent", "engine", "claude", ""} {
		err := eng.GrantWaiver(context.Background(), actor, "run_x", "n1", "g1", "because", time.Now().Add(time.Hour))
		if err == nil {
			t.Errorf("GrantWaiver(actor=%q) = nil error, want a rejection — only \"human\" may grant a waiver", actor)
		}
	}
}

// TestEngine_grantWaiverRequiresAReason proves the doc's "a waiver is an
// event with an author, a reason, and an expiry" as an enforced
// invariant, not just a struct field nobody checks.
func TestEngine_grantWaiverRequiresAReason(t *testing.T) {
	st := openStore(t)
	workRoot := t.TempDir()
	eng := newTestEngine(t, st, local.New(local.DefaultBootIDProvider()), local.DefaultBootIDProvider(), workRoot)

	err := eng.GrantWaiver(context.Background(), "human", "run_x", "n1", "g1", "", time.Now().Add(time.Hour))
	if err == nil {
		t.Fatal("GrantWaiver with an empty reason succeeded, want a rejection")
	}
}
