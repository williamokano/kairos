package engine_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/admission"
	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/engine"
	"github.com/williamokano/kairos/internal/eventstore"
	"github.com/williamokano/kairos/internal/executor/local"
)

func writeShellDef(t *testing.T, dir, name, script string) string {
	t.Helper()
	defPath := filepath.Join(dir, name+".yaml")
	indented := strings.ReplaceAll(script, "\n", "\n      ")
	yaml := "name: " + name + "\n" +
		"nodes:\n" +
		"  - id: n1\n" +
		"    actor: shell\n" +
		"    prompt: |\n" +
		"      " + indented + "\n" +
		"    output: { ok: \"bool!\" }\n"
	if err := os.WriteFile(defPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}
	return defPath
}

func oneNodeGraph() domain.Graph {
	return domain.Graph{
		Entry: "n1",
		Nodes: []domain.Node{{ID: "n1", Retry: domain.RetryPolicy{MaxAttempts: 1}, LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 1}}},
		Edges: map[domain.NodeID]map[domain.EdgeTrigger]domain.NodeID{
			"n1": {domain.OnSuccess: domain.SinkSucceed, domain.OnFailure: domain.SinkFail, domain.OnTimeout: domain.SinkFail},
		},
	}
}

// seedTriggerAndStart appends TriggerReceived+RunStarted for a fresh
// one-node run, which is enough for the engine's live loop to dispatch
// n1 through dispatchStartNode (and therefore through admission).
func seedTriggerAndStart(t *testing.T, ctx context.Context, st eventstore.Store, runID, defPath string) {
	t.Helper()
	meta := eventstore.AppendMeta{Actor: "test", CorrelationID: runID, OccurredAt: time.Now()}
	if _, err := st.AppendIf(ctx, runID, 0, []domain.Event{
		domain.TriggerReceived{RunID: runID, TriggerRef: "test", DefinitionRef: defPath, CorrelationID: runID},
	}, meta); err != nil {
		t.Fatalf("append trigger: %v", err)
	}
	if _, err := st.AppendIf(ctx, runID, 1, []domain.Event{
		domain.RunStarted{RunID: runID, Graph: oneNodeGraph()},
	}, meta); err != nil {
		t.Fatalf("append run started: %v", err)
	}
}

// nodeExecutionFailedMessage scans runID's stream for its NodeExecutionFailed
// event and returns its Message — domain.Advance deliberately discards the
// Message field when folding into RunState (handleFailureOutcome's `_`
// parameter), so a denial reason can only be observed from the raw event
// log, not the projection.
func nodeExecutionFailedMessage(t *testing.T, ctx context.Context, st eventstore.Store, runID string) string {
	t.Helper()
	envs, err := st.Read(ctx, runID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, env := range envs {
		if f, ok := env.Event.(domain.NodeExecutionFailed); ok {
			return f.Message
		}
	}
	t.Fatalf("no NodeExecutionFailed event found in run %s", runID)
	return ""
}

// TestEngine_admissionDeniesPastCapacityWithReason drives three
// single-node runs at NodeSlots:1, MaxQueued:1: the first is Granted and
// holds the sole slot for a couple of seconds, the second is genuinely
// Queued (the engine's queue is empty when it arrives), and the third
// arrives while the queue already holds one entry — rule 7 (queued >=
// maxQueued -> REJECT, not queue) must Deny it outright. Asserts the
// resulting NodeExecutionFailed carries the denial reason verbatim rather
// than a generic failure message.
func TestEngine_admissionDeniesPastCapacityWithReason(t *testing.T) {
	workRoot := t.TempDir()
	defDir := t.TempDir()

	st := openStore(t)
	eng := engine.New(engine.Config{
		Store:     st,
		Executor:  local.New(local.DefaultBootIDProvider()),
		BootID:    local.DefaultBootIDProvider(),
		WorkRoot:  workRoot,
		KillGrace: 200 * time.Millisecond,
		Admission: admission.Config{NodeSlots: 1, MaxQueued: 1},
		Logger:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := eng.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = eng.Stop(context.Background()) }()

	slowDef := writeShellDef(t, defDir, "slow", "sleep 2\necho '{\"ok\":true}' > \"$KAIROS_OUTPUT_PATH\"")
	queuedDef := writeShellDef(t, defDir, "queued2", "echo '{\"ok\":true}' > \"$KAIROS_OUTPUT_PATH\"")
	deniedDef := writeShellDef(t, defDir, "denied", "echo '{\"ok\":true}' > \"$KAIROS_OUTPUT_PATH\"")

	seedTriggerAndStart(t, ctx, st, "run_slow", slowDef)
	// Give the first run a head start so it reliably claims the sole slot
	// before the next two runs' admission checks run.
	time.Sleep(150 * time.Millisecond)
	seedTriggerAndStart(t, ctx, st, "run_queued2", queuedDef)
	time.Sleep(150 * time.Millisecond) // let run_queued2 land in the pending queue
	seedTriggerAndStart(t, ctx, st, "run_denied", deniedDef)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		state, ok, err := st.GetRunState(ctx, "run_denied")
		if err != nil {
			t.Fatalf("GetRunState: %v", err)
		}
		if ok && state.Status.Terminal() {
			if state.Status != domain.RunFailed {
				t.Fatalf("run_denied Status = %s, want %s", state.Status, domain.RunFailed)
			}
			msg := nodeExecutionFailedMessage(t, ctx, st, "run_denied")
			if !strings.HasPrefix(msg, "denied: ") {
				t.Errorf("Message = %q, want it to start with \"denied: \"", msg)
			}
			if !strings.Contains(msg, "queue full") {
				t.Errorf("Message = %q, want it to name the queue-full reason", msg)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("run_denied did not reach a terminal state within the deadline")
}

// TestEngine_admissionQueuesThenRunsOnceASlotFrees drives two single-node
// runs at NodeSlots:1, MaxQueued:10 — the second run's node execution must
// be Queued, not Denied, and must eventually run and succeed once the
// first run's node releases its slot. This proves drainPending actually
// retries a queued node, not just that admission blocks it.
func TestEngine_admissionQueuesThenRunsOnceASlotFrees(t *testing.T) {
	workRoot := t.TempDir()
	defDir := t.TempDir()

	st := openStore(t)
	eng := engine.New(engine.Config{
		Store:     st,
		Executor:  local.New(local.DefaultBootIDProvider()),
		BootID:    local.DefaultBootIDProvider(),
		WorkRoot:  workRoot,
		KillGrace: 200 * time.Millisecond,
		Admission: admission.Config{NodeSlots: 1, MaxQueued: 10},
		Logger:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := eng.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = eng.Stop(context.Background()) }()

	briefDef := writeShellDef(t, defDir, "brief", "sleep 1\necho '{\"ok\":true}' > \"$KAIROS_OUTPUT_PATH\"")
	queuedDef := writeShellDef(t, defDir, "queued", "echo '{\"ok\":true}' > \"$KAIROS_OUTPUT_PATH\"")

	seedTriggerAndStart(t, ctx, st, "run_brief", briefDef)
	time.Sleep(150 * time.Millisecond)
	seedTriggerAndStart(t, ctx, st, "run_queued", queuedDef)

	// While run_brief still holds the slot, run_queued's node must not
	// have started yet — proving it was genuinely queued, not just slow
	// to admit.
	state, ok, err := st.GetRunState(ctx, "run_queued")
	if err != nil {
		t.Fatalf("GetRunState: %v", err)
	}
	if ok {
		if execs, has := state.Executions["n1"]; has && len(execs) > 0 && execs[len(execs)-1].Status != domain.ExecPending {
			t.Fatalf("run_queued's node started before the slot freed: %+v", execs[len(execs)-1])
		}
	}

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		s, ok, err := st.GetRunState(ctx, "run_queued")
		if err != nil {
			t.Fatalf("GetRunState: %v", err)
		}
		if ok && s.Status.Terminal() {
			if s.Status != domain.RunSucceeded {
				t.Fatalf("run_queued Status = %s, want %s; state=%+v", s.Status, domain.RunSucceeded, s)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("run_queued did not reach a terminal state within the deadline")
}
