package engine_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/engine"
	"github.com/williamokano/kairos/internal/executor/local"
)

// newNativeResumeTestRepo creates a real, minimal git repo — mirrors
// internal/workspace's own newTestRepo fixture (unexported there, in a
// different package, so this is a small local copy rather than an
// import).
func newNativeResumeTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatalf("writing README: %v", err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-q", "-m", "initial")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out.String())
	}
}

// TestEngine_llmActorUsesNativeResumeFlagOnSecondAttempt proves 04-agents.md's
// "native" resume mode is a real part of the invocation, not just an
// audit env var: the fake CLI is scripted to fail unless invoked with
// --resume as its first argument, so the run can ONLY succeed if attempt
// 2's argv genuinely carries the native resume flag. sessionAffinity:
// node plus workspace: write is what gives resolveSession a stable Dir
// across attempts (04-agents.md's "path-keying trap" — a non-write node's
// scratch dir is attempt-specific and would never resume, by design).
func TestEngine_llmActorUsesNativeResumeFlagOnSecondAttempt(t *testing.T) {
	repo := newNativeResumeTestRepo(t)
	st := openStore(t)
	// home nests workRoot one level below its own dedicated tempdir (the
	// same convention internal/workspace's own tests use) so the engine's
	// default MirrorRoot (filepath.Dir(workRoot)) lands on `home`, not on
	// the shared parent t.TempDir() calls in this test function all
	// share — repo is ALSO a t.TempDir() call, and if workRoot were used
	// bare, filepath.Dir(workRoot) would equal that shared parent and
	// legitimately trip ADR 0005's "inside kairos's own state directory"
	// refusal, since repo would then genuinely be nested inside it.
	home := t.TempDir()
	workRoot := filepath.Join(home, "work")
	runID := "run_native_resume"

	fakeCLI := writeFakeLLM(t, `
if [ "$1" = "--resume" ] && [ -n "$2" ]; then
  echo '{"ok":true}' > "$KAIROS_OUTPUT"
  exit 0
fi
exit 1
`)

	eng := engine.New(engine.Config{
		Store:         st,
		Executor:      local.New(local.DefaultBootIDProvider()),
		BootID:        local.DefaultBootIDProvider(),
		WorkRoot:      workRoot,
		WorkspaceRepo: repo,
		LLMBinary:     fakeCLI,
		KillGrace:     200 * time.Millisecond,
	})

	yaml := `
name: native-resume
nodes:
  - id: n1
    actor: claude
    workspace: write
    sessionAffinity: node
    retry:
      maxAttempts: 2
      retryOn: [failure]
    output: { ok: "bool!" }
`

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := eng.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = eng.Stop(context.Background()) }()

	// The run must be started only AFTER Start subscribes — Reconcile
	// never picks up a Pending exec that was never dispatched in the
	// first place (it only recovers Executing ones), so appending the
	// trigger before the live loop exists would strand the run forever.
	writeAndStartRun(t, st, workRoot, runID, yaml)

	status := waitForTerminal(t, ctx, st, runID, 8*time.Second)
	if status != domain.RunSucceeded {
		t.Fatalf("run Status = %s, want %s — attempt 2 apparently did not receive --resume", status, domain.RunSucceeded)
	}
}
