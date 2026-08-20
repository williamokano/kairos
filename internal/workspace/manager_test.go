package workspace_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/williamokano/kairos/internal/executor/local"
	"github.com/williamokano/kairos/internal/workspace"
)

// newTestRepo creates a real, minimal git repo with one commit at a fresh
// temp dir and returns its path — the "source repo" every test provisions
// a workspace from.
func newTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "git", "init", "-q")
	run(t, dir, "git", "config", "user.email", "test@example.com")
	run(t, dir, "git", "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatalf("writing README: %v", err)
	}
	run(t, dir, "git", "add", "README.md")
	run(t, dir, "git", "commit", "-q", "-m", "initial")
	return dir
}

// run shells out directly (not through the executor) purely as test
// scaffolding to build the fixture repo — internal/workspace itself never
// does this; its own git invocations all go through local.Executor.
func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out.String())
	}
}

func newManager(t *testing.T) (*workspace.Manager, string) {
	t.Helper()
	home := t.TempDir()
	mirrorRoot := filepath.Join(home, "mirrors")
	workRoot := filepath.Join(home, "work")
	exec := local.New(local.DefaultBootIDProvider())
	return workspace.New(mirrorRoot, workRoot, exec), workRoot
}

func TestManager_provisionClonesViaReferenceAndSharesObjects(t *testing.T) {
	repo := newTestRepo(t)
	mgr, _ := newManager(t)
	ctx := context.Background()

	ws, err := mgr.Provision(ctx, "run1", repo)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if !workspace.Verify(ws.Dir) {
		t.Fatalf("Verify(%q) = false, want true", ws.Dir)
	}

	alternates, err := os.ReadFile(filepath.Join(ws.Dir, ".git", "objects", "info", "alternates"))
	if err != nil {
		t.Fatalf("reading alternates: %v", err)
	}
	if len(bytes.TrimSpace(alternates)) == 0 {
		t.Error("alternates file is empty — the clone did not borrow objects via --reference")
	}

	// The clone must actually contain the committed content.
	body, err := os.ReadFile(filepath.Join(ws.Dir, "README.md"))
	if err != nil {
		t.Fatalf("reading README from clone: %v", err)
	}
	if string(body) != "hello\n" {
		t.Errorf("README content = %q", string(body))
	}
}

func TestManager_provisionIsIdempotent(t *testing.T) {
	repo := newTestRepo(t)
	mgr, _ := newManager(t)
	ctx := context.Background()

	ws1, err := mgr.Provision(ctx, "run1", repo)
	if err != nil {
		t.Fatalf("first Provision: %v", err)
	}
	// Mark the workspace with a file that a re-clone would wipe, to prove
	// the second call is a no-op rather than a fresh clone.
	marker := filepath.Join(ws1.Dir, "untracked-marker")
	if err := os.WriteFile(marker, []byte("x"), 0o600); err != nil {
		t.Fatalf("writing marker: %v", err)
	}

	ws2, err := mgr.Provision(ctx, "run1", repo)
	if err != nil {
		t.Fatalf("second Provision: %v", err)
	}
	if ws2.Dir != ws1.Dir {
		t.Fatalf("Dir changed across idempotent Provision calls: %q vs %q", ws1.Dir, ws2.Dir)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Error("idempotent Provision destroyed the existing workspace instead of reusing it")
	}
}

func TestManager_reprovisionReplacesACorruptWorkspace(t *testing.T) {
	repo := newTestRepo(t)
	mgr, _ := newManager(t)
	ctx := context.Background()

	ws, err := mgr.Provision(ctx, "run1", repo)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(ws.Dir, ".git")); err != nil {
		t.Fatalf("simulating corruption: %v", err)
	}
	if workspace.Verify(ws.Dir) {
		t.Fatal("Verify should report false for a workspace missing .git")
	}

	ws2, err := mgr.Reprovision(ctx, "run1", repo)
	if err != nil {
		t.Fatalf("Reprovision: %v", err)
	}
	if !workspace.Verify(ws2.Dir) {
		t.Fatal("Reprovision did not leave an intact workspace")
	}
	if _, err := os.Stat(filepath.Join(ws2.Dir, "README.md")); err != nil {
		t.Errorf("Reprovisioned workspace missing repo content: %v", err)
	}
}

func TestManager_gcRemovesOnlyInactiveRunDirs(t *testing.T) {
	repo := newTestRepo(t)
	mgr, workRoot := newManager(t)
	ctx := context.Background()

	if _, err := mgr.Provision(ctx, "run-active", repo); err != nil {
		t.Fatalf("Provision run-active: %v", err)
	}
	if _, err := mgr.Provision(ctx, "run-stale", repo); err != nil {
		t.Fatalf("Provision run-stale: %v", err)
	}

	removed, err := mgr.GC(ctx, map[string]bool{"run-active": true})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(removed) != 1 || removed[0] != "run-stale" {
		t.Errorf("removed = %v, want [run-stale]", removed)
	}
	if _, err := os.Stat(filepath.Join(workRoot, "run-active")); err != nil {
		t.Error("GC removed the active run's directory")
	}
	if _, err := os.Stat(filepath.Join(workRoot, "run-stale")); !os.IsNotExist(err) {
		t.Error("GC did not remove the stale run's directory")
	}
}

func TestManager_provisionRefusesASourceInsideKairosOwnStateDir(t *testing.T) {
	home := t.TempDir()
	kairosHome := filepath.Join(home, ".kairos")
	repoInsideState := filepath.Join(kairosHome, "somewhere")
	if err := os.MkdirAll(filepath.Join(repoInsideState, ".git"), 0o700); err != nil {
		t.Fatalf("MkdirAll .git: %v", err)
	}

	mgr := workspace.New(filepath.Join(kairosHome, "mirrors"), filepath.Join(kairosHome, "work"), local.New(local.DefaultBootIDProvider()))
	if _, err := mgr.Provision(context.Background(), "run1", repoInsideState); err == nil {
		t.Fatal("expected Provision to refuse a source inside kairos's own state directory")
	}
}

// TestManager_provisionAppliesTheCredentialGuard proves 04-agents.md's
// "capability, not permission" design is actually in place before any
// actor ever runs inside the workspace: origin's pushurl is blocked, and
// the credential helper/hooks/askpass paths that could otherwise leak an
// ambient credential are all disabled.
func TestManager_provisionAppliesTheCredentialGuard(t *testing.T) {
	repo := newTestRepo(t)
	mgr, _ := newManager(t)
	ctx := context.Background()

	ws, err := mgr.Provision(ctx, "run1", repo)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	cases := map[string]string{
		"credential.helper":     "",
		"core.hooksPath":        "/dev/null",
		"core.askPass":          "/usr/bin/false",
		"remote.origin.pushurl": "kairos-blocked://denied",
	}
	for key, want := range cases {
		cmd := exec.Command("git", "config", "--local", "--get", key)
		cmd.Dir = ws.Dir
		out, err := cmd.Output()
		got := string(bytes.TrimSpace(out))
		if key == "credential.helper" {
			// An empty value entry ("[credential] helper =") makes
			// `git config --get` exit 1 (a set-but-empty value doesn't
			// count as "found" for --get) — the file having the key at
			// all is the guard; assert via --get-all's exit code instead.
			if err != nil {
				t.Errorf("credential.helper: git config --get failed: %v (want the key present, even if empty)", err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: git config --get failed: %v", key, err)
			continue
		}
		if got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}
