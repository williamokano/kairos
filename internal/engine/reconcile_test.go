package engine_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/engine"
	"github.com/williamokano/kairos/internal/events"
	"github.com/williamokano/kairos/internal/eventstore"
	"github.com/williamokano/kairos/internal/executor/exectest"
	"github.com/williamokano/kairos/internal/executor/local"
)

// fakeBootID lets tests control what "the current boot" looks like,
// independent of the real host's actual boot id.
type fakeBootID struct{ id string }

func (f fakeBootID) Current() (string, error) { return f.id, nil }

func openStore(t *testing.T) eventstore.Store {
	t.Helper()
	registry, err := events.Builtin()
	if err != nil {
		t.Fatalf("Builtin: %v", err)
	}
	st, err := eventstore.Open(context.Background(), eventstore.Config{
		Path:     filepath.Join(t.TempDir(), "kairos.db"),
		Registry: registry,
		Projections: []eventstore.Projection{
			eventstore.RunStateProjection{},
			eventstore.RunIndexProjection{},
		},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func linearShellGraph() domain.Graph {
	return domain.Graph{
		Entry: "n1",
		Nodes: []domain.Node{{ID: "n1", Retry: domain.RetryPolicy{MaxAttempts: 2}, LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 1}}},
		Edges: map[domain.NodeID]map[domain.EdgeTrigger]domain.NodeID{
			"n1": {domain.OnSuccess: domain.SinkSucceed, domain.OnFailure: domain.SinkFail, domain.OnTimeout: domain.SinkFail},
		},
	}
}

// seedExecutingRun appends TriggerReceived+RunStarted+NodeExecutionStarted
// directly to store, landing a run whose sole node is ExecExecuting —
// exactly the state reconciliation must resolve at boot.
func seedExecutingRun(t *testing.T, st eventstore.Store, runID, definitionRef string) string {
	t.Helper()
	ctx := context.Background()
	meta := eventstore.AppendMeta{Actor: "test", CorrelationID: runID, OccurredAt: time.Unix(0, 0)}

	if _, err := st.AppendIf(ctx, runID, 0, []domain.Event{
		domain.TriggerReceived{RunID: runID, TriggerRef: "test", DefinitionRef: definitionRef, CorrelationID: runID},
	}, meta); err != nil {
		t.Fatalf("append trigger: %v", err)
	}
	if _, err := st.AppendIf(ctx, runID, 1, []domain.Event{
		domain.RunStarted{RunID: runID, Graph: linearShellGraph()},
	}, meta); err != nil {
		t.Fatalf("append run started: %v", err)
	}
	execID := "n1#a1.i1"
	if _, err := st.AppendIf(ctx, runID, 2, []domain.Event{
		domain.NodeExecutionStarted{RunID: runID, NodeID: "n1", ExecID: execID, Attempt: 1, Iteration: 1},
	}, meta); err != nil {
		t.Fatalf("append node execution started: %v", err)
	}
	return execID
}

func newTestEngine(t *testing.T, st eventstore.Store, exec local.Executor, bootID local.BootIDProvider, workRoot string) *engine.Engine {
	t.Helper()
	return engine.New(engine.Config{
		Store:     st,
		Executor:  exec,
		BootID:    bootID,
		WorkRoot:  workRoot,
		KillGrace: 200 * time.Millisecond,
		Logger:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	})
}

func TestReconcile_rebootInvalidatesRecordedPGIDs(t *testing.T) {
	st := openStore(t)
	workRoot := t.TempDir()
	runID := "run_stale"

	// fix-issue-shaped: sideEffectFree unset -> fail-to-human default,
	// but reconciliation should still record Lost regardless of policy.
	defPath := writeMilestoneLikeDefinition(t, workRoot)
	execID := seedExecutingRun(t, st, runID, defPath)

	// Seed a proc.json recorded under an OLD boot id.
	dir := filepath.Join(workRoot, runID, execID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeProcJSON(t, dir, local.ProcRecord{PID: 99999, PGID: 99999, BootID: "old-boot", StartedAt: time.Now()})

	fake := exectest.NewFake()
	eng := newTestEngine(t, st, fake, fakeBootID{id: "new-boot"}, workRoot)

	report, err := eng.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if report.Lost != 1 {
		t.Errorf("report.Lost = %d, want 1", report.Lost)
	}

	if sigs := fake.Signals(); len(sigs) != 0 {
		t.Errorf("expected kill to never be called on a mismatched-boot pgid, got signals: %+v", sigs)
	}

	envs, err := st.Read(context.Background(), runID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	found := false
	for _, e := range envs {
		if e.EventType == "node.execution.lost" {
			found = true
		}
	}
	if !found {
		t.Error("expected node.execution.lost in the run's stream")
	}
}

func TestReconcile_orphanedAliveProcessIsKilledAndMarkedLost(t *testing.T) {
	st := openStore(t)
	workRoot := t.TempDir()
	runID := "run_orphan"
	defPath := writeMilestoneLikeDefinition(t, workRoot)
	execID := seedExecutingRun(t, st, runID, defPath)

	dir := filepath.Join(workRoot, runID, execID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	fake := exectest.NewFake()
	eng := newTestEngine(t, st, fake, fakeBootID{id: "same-boot"}, workRoot)

	// local.Probe's liveness check (identity.go) uses the real syscall
	// directly, independent of the injected Executor — so exercising the
	// Alive branch needs a genuinely alive pgid. The test process's own
	// pgid is a safe, harmless stand-in: the actual "kill" action goes
	// through e.exec.Signal, which is the fake (recorded, never real).
	ownPGID, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		t.Fatalf("Getpgid: %v", err)
	}
	writeProcJSON(t, dir, local.ProcRecord{PID: ownPGID, PGID: ownPGID, BootID: "same-boot", StartedAt: time.Now()})

	report, err := eng.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if report.OrphansReaped != 1 {
		t.Errorf("report.OrphansReaped = %d, want 1", report.OrphansReaped)
	}
	if report.Lost != 1 {
		t.Errorf("report.Lost = %d, want 1", report.Lost)
	}

	sigs := fake.Signals()
	if len(sigs) != 1 || sigs[0].PGID != ownPGID {
		t.Errorf("Signals() = %+v, want exactly one SIGKILL to pgid %d", sigs, ownPGID)
	}

	systemEnvs, err := st.Read(context.Background(), "system")
	if err != nil {
		t.Fatalf("Read(system): %v", err)
	}
	sawOrphanReaped := false
	for _, e := range systemEnvs {
		if e.EventType == "process.orphan.reaped" {
			sawOrphanReaped = true
		}
	}
	if !sawOrphanReaped {
		t.Error("expected process.orphan.reaped on the system stream")
	}
}

// TestReconcile_adoptedAliveProcessIsNotKilledAndItsExitIsFolded proves
// RestartPolicy: adopt's whole point: reconciliation must not kill an
// alive orphan whose node opted into adoption, and must fold its real,
// natural exit through the normal output path once it happens — no
// node.execution.lost, no retry, since nothing was ever lost.
func TestReconcile_adoptedAliveProcessIsNotKilledAndItsExitIsFolded(t *testing.T) {
	st := openStore(t)
	workRoot := t.TempDir()
	runID := "run_adopt"
	defPath := writeAdoptDefinition(t, workRoot)
	execID := seedExecutingRun(t, st, runID, defPath)

	dir := filepath.Join(workRoot, runID, execID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	outputPath := filepath.Join(dir, "output.json")

	// A real, short-lived child in its own process group — Probe's
	// liveness check is a real syscall, independent of any injected
	// Executor, so adoption needs a genuinely alive-then-dead process to
	// prove anything.
	cmd := exec.Command("/bin/sh", "-c", "sleep 0.3; printf '{\"ok\":true}' > \""+outputPath+"\"")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting adopted process: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	// A real orphan gets reparented to init, which reaps it promptly on
	// exit — kill(pgid, 0) only reports a process dead once it's reaped,
	// not merely exited. This test owns the child directly (it was never
	// actually orphaned), so it must reap it itself to model that.
	go func() { _, _ = cmd.Process.Wait() }()
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("Getpgid: %v", err)
	}
	writeProcJSON(t, dir, local.ProcRecord{PID: cmd.Process.Pid, PGID: pgid, BootID: "same-boot", StartedAt: time.Now()})

	fake := exectest.NewFake()
	eng := newTestEngine(t, st, fake, fakeBootID{id: "same-boot"}, workRoot)

	report, err := eng.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if report.OrphansReaped != 0 {
		t.Errorf("report.OrphansReaped = %d, want 0 — an adopted process must not be reaped", report.OrphansReaped)
	}
	if report.Lost != 0 {
		t.Errorf("report.Lost = %d, want 0 — an adopted process's execution was never lost", report.Lost)
	}
	if sigs := fake.Signals(); len(sigs) != 0 {
		t.Errorf("expected no signal sent to an adopted process, got: %+v", sigs)
	}

	deadline := time.Now().Add(5 * time.Second)
	var sawOutputReceived, sawLost bool
	for time.Now().Before(deadline) {
		envs, err := st.Read(context.Background(), runID)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		for _, e := range envs {
			switch e.EventType {
			case "node.output.received":
				sawOutputReceived = true
			case "node.execution.lost":
				sawLost = true
			}
		}
		if sawOutputReceived {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !sawOutputReceived {
		t.Fatal("the adopted process's natural exit was never folded into the run")
	}
	if sawLost {
		t.Error("adoption must never record node.execution.lost")
	}
}

// writeAdoptDefinition is writeMilestoneLikeDefinition's twin with
// restartPolicy: adopt declared explicitly.
func writeAdoptDefinition(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "adopt-def.yaml")
	yaml := `
name: adopt-test
nodes:
  - id: n1
    actor: shell
    restartPolicy: adopt
    prompt: "true"
    output: { ok: "bool!" }
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}
	return path
}

func writeProcJSON(t *testing.T, dir string, rec local.ProcRecord) {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, "proc.json"))
	if err != nil {
		t.Fatalf("creating proc.json: %v", err)
	}
	defer func() { _ = f.Close() }()
	if err := json.NewEncoder(f).Encode(rec); err != nil {
		t.Fatalf("encoding proc.json: %v", err)
	}
}

// writeMilestoneLikeDefinition writes a minimal one-shell-node workflow
// definition matching linearShellGraph's shape, so dispatch's
// registry.Load(definitionRef) call succeeds during recovery re-dispatch.
func writeMilestoneLikeDefinition(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "def.yaml")
	yaml := `
name: reconcile-test
nodes:
  - id: n1
    actor: shell
    prompt: "echo '{}' > \"$KAIROS_OUTPUT_PATH\""
    output: { x: "string" }
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}
	return path
}
