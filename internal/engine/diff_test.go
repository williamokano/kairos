package engine_test

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/engine"
	"github.com/williamokano/kairos/internal/executor/local"
)

// diffWorkflowDefPath is a two-node workspace: write workflow, like
// twoWriteNodeDef, but n2 additionally declares a workspacePaths scope
// that does NOT cover the file it writes — proving Diff's
// scope-violation detection against a real, deliberately-out-of-scope
// write, not a fixture that never exercises the banner at all.
func diffWorkflowDefPath(t *testing.T) string {
	t.Helper()
	defPath := filepath.Join(t.TempDir(), "diff-e2e.yaml")
	yaml := `
name: diff-e2e
nodes:
  - id: n1
    actor: shell
    workspace: write
    prompt: |
      echo line1 >> log.txt
      echo '{"lines":2}' > "$KAIROS_OUTPUT_PATH"
    output: { lines: "int!" }
  - id: n2
    actor: shell
    workspace: write
    workspacePaths: ["notes/**"]
    prompt: |
      echo line2 >> log.txt
      echo '{"lines":3}' > "$KAIROS_OUTPUT_PATH"
    output: { lines: "int!" }
`
	if err := os.WriteFile(defPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}
	return defPath
}

// runDiffFixture provisions a real git source repo, runs
// diffWorkflowDefPath to completion through a real engine, and returns
// the engine, the run id, and the source commit baseRef — enough for
// every test below to call Engine.Diff directly.
func runDiffFixture(t *testing.T) (eng *engine.Engine, runID, baseRef string) {
	t.Helper()
	sourceRepo := newSourceRepoDir(t)
	initGitRepo(t, sourceRepo)
	baseRef = gitRevParseHEAD(t, sourceRepo)

	workRoot := t.TempDir()
	st := openStore(t)
	eng = engine.New(engine.Config{
		Store: st, Executor: local.New(local.DefaultBootIDProvider()),
		BootID: local.DefaultBootIDProvider(), WorkRoot: workRoot,
		WorkspaceRepo: sourceRepo, BaseRef: baseRef,
		KillGrace: 200 * time.Millisecond,
		Logger:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := eng.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = eng.Stop(context.Background()) })

	runID = "run_diffe2e"
	meta := appendMetaFor(runID)
	defPath := diffWorkflowDefPath(t)
	if _, err := st.AppendIf(ctx, runID, 0, []domain.Event{
		domain.TriggerReceived{RunID: runID, TriggerRef: "test", DefinitionRef: defPath, CorrelationID: runID},
	}, meta); err != nil {
		t.Fatalf("append trigger: %v", err)
	}
	if _, err := st.AppendIf(ctx, runID, 1, []domain.Event{
		domain.RunStarted{RunID: runID, Graph: twoNodeGraph()},
	}, meta); err != nil {
		t.Fatalf("append run started: %v", err)
	}

	final := runToTerminal(t, ctx, st, runID, 15*time.Second)
	if final.Status != domain.RunSucceeded {
		t.Fatalf("fixture run Status = %s, want succeeded; state=%+v", final.Status, final)
	}

	// Real flake, found under -race with concurrent CPU load: a node's
	// own success (NodeOutputReceived, what runToTerminal above waits
	// for) and maybeSnapshotWorkspace's own WorkspaceSnapshotTaken append
	// are two separate, sequential appends on the SAME reapShell
	// goroutine — genuinely durable, but not atomic with each other
	// (ADR 0006: the snapshot is deliberately best-effort, decoupled from
	// the node's own success/failure). n2 is this fixture's LAST node, so
	// the run can already read as RunSucceeded a moment before n2's own
	// snapshot has actually landed; every test below reads Engine.Diff
	// right after this fixture returns and genuinely needs both
	// snapshots to already be on the log, so wait for them explicitly
	// rather than assuming "the run finished" already implies it.
	deadline := time.Now().Add(15 * time.Second)
	for {
		envs, err := st.Read(ctx, runID)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		var snaps int
		for _, e := range envs {
			if _, ok := e.Event.(domain.WorkspaceSnapshotTaken); ok {
				snaps++
			}
		}
		if snaps >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("workspace.snapshot.taken count = %d, want 2 (one per workspace: write node)", snaps)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return eng, runID, baseRef
}

func gitRevParseHEAD(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func TestEngine_diffWholeRunAgainstBaseRef(t *testing.T) {
	eng, runID, baseRef := runDiffFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := eng.Diff(ctx, engine.DiffRequest{RunID: runID})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if result.FromRef != baseRef {
		t.Errorf("FromRef = %q, want the source commit %q", result.FromRef, baseRef)
	}
	if len(result.Files) != 1 || result.Files[0].Path != "log.txt" {
		t.Fatalf("Files = %+v, want exactly one entry for log.txt", result.Files)
	}
	if result.Files[0].Added != 2 {
		t.Errorf("log.txt Added = %d, want 2 (both nodes' appended lines)", result.Files[0].Added)
	}
	if !strings.Contains(result.Patch, "+line1") || !strings.Contains(result.Patch, "+line2") {
		t.Errorf("Patch missing an expected added line: %s", result.Patch)
	}
	// The whole-run view has no single node's declared scope to check
	// against, so it must never claim a violation.
	if len(result.ScopeViolations) != 0 {
		t.Errorf("whole-run Diff reported ScopeViolations = %v, want none (no single node's scope applies)", result.ScopeViolations)
	}
}

func TestEngine_diffOneNodeSeesOnlyItsOwnChange(t *testing.T) {
	eng, runID, baseRef := runDiffFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	n1, err := eng.Diff(ctx, engine.DiffRequest{RunID: runID, NodeID: "n1"})
	if err != nil {
		t.Fatalf("Diff(n1): %v", err)
	}
	if n1.FromRef != baseRef {
		t.Errorf("n1.FromRef = %q, want the run's base ref %q (n1 is the first workspace write)", n1.FromRef, baseRef)
	}
	if !strings.Contains(n1.Patch, "+line1") || strings.Contains(n1.Patch, "+line2") {
		t.Errorf("n1's diff should show only line1, got: %s", n1.Patch)
	}

	n2, err := eng.Diff(ctx, engine.DiffRequest{RunID: runID, NodeID: "n2"})
	if err != nil {
		t.Fatalf("Diff(n2): %v", err)
	}
	if n2.FromRef == baseRef {
		t.Errorf("n2.FromRef = %q, want n1's own snapshot SHA, not the run's base ref", n2.FromRef)
	}
	if strings.Contains(n2.Patch, "+line1") {
		t.Errorf("n2's diff should show only its OWN change (line2), not n1's — got: %s", n2.Patch)
	}
	if !strings.Contains(n2.Patch, "+line2") {
		t.Errorf("n2's diff missing +line2: %s", n2.Patch)
	}

	// n2 declares workspacePaths: ["notes/**"] but wrote log.txt — a real,
	// deliberately-provoked scope violation, not an untested code path.
	if len(n2.ScopeViolations) != 1 || n2.ScopeViolations[0] != "log.txt" {
		t.Errorf("n2.ScopeViolations = %v, want [\"log.txt\"]", n2.ScopeViolations)
	}
}

func TestEngine_diffUnknownNodeReturnsErrNoWorkspaceSnapshot(t *testing.T) {
	eng, runID, _ := runDiffFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := eng.Diff(ctx, engine.DiffRequest{RunID: runID, NodeID: "no-such-node"})
	if err == nil {
		t.Fatal("Diff for a node with no recorded snapshot returned no error")
	}
	if !strings.Contains(err.Error(), "no workspace snapshot recorded") {
		t.Errorf("error = %v, want ErrNoWorkspaceSnapshot's message", err)
	}
}
