package workspace_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/williamokano/kairos/internal/executor/local"
	"github.com/williamokano/kairos/internal/workspace"
)

func TestProbeCoWSupport_realProbeOnThisHost(t *testing.T) {
	dir := t.TempDir()
	// Just confirm it runs without panicking and returns a stable answer
	// twice in a row — the actual bool depends on the CI/dev host's
	// filesystem, which is exactly ADR 0006's point (probe, don't assume).
	a := local.ProbeCoWSupport(dir)
	b := local.ProbeCoWSupport(dir)
	if a != b {
		t.Errorf("ProbeCoWSupport not stable across calls: %v then %v", a, b)
	}
	t.Logf("CoW support on %s: %v", dir, a)
}

func newTestManagerWithRepo(t *testing.T) (*workspace.Manager, string, workspace.Workspace) {
	t.Helper()
	srcRepo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = srcRepo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(srcRepo, "file.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "initial")

	home := t.TempDir()
	m := workspace.New(filepath.Join(home, "mirrors"), filepath.Join(home, "work"), local.New(local.DefaultBootIDProvider()))
	ws, err := m.Provision(context.Background(), "run_snaptest", srcRepo)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	return m, srcRepo, ws
}

func TestSnapshotGitRef_capturesCurrentTreeWithoutTouchingWorkingState(t *testing.T) {
	m, _, ws := newTestManagerWithRepo(t)
	ctx := context.Background()

	// Dirty the working tree with an uncommitted, untracked file — the
	// snapshot must capture it (git add -A stages everything into the
	// throwaway index) without ever touching the real index/HEAD.
	if err := os.WriteFile(filepath.Join(ws.Dir, "untracked.txt"), []byte("uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}

	statusBefore := gitStatus(t, ws.Dir)

	snap, err := m.SnapshotGitRef(ctx, ws, 5)
	if err != nil {
		t.Fatalf("SnapshotGitRef: %v", err)
	}
	if snap.Ref != "refs/kairos/runs/run_snaptest/5" {
		t.Errorf("Ref = %q, want refs/kairos/runs/run_snaptest/5", snap.Ref)
	}
	if snap.SHA == "" {
		t.Error("expected a non-empty SHA")
	}

	statusAfter := gitStatus(t, ws.Dir)
	if statusBefore != statusAfter {
		t.Errorf("snapshot mutated working tree status:\nbefore: %q\nafter:  %q", statusBefore, statusAfter)
	}

	// The ref must be a real, inspectable git object — "git diff" against
	// it must work with tools the operator already has.
	out, err := exec.Command("git", "-C", ws.Dir, "cat-file", "-p", snap.SHA).CombinedOutput()
	if err != nil {
		t.Fatalf("cat-file on snapshot commit: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "tree ") {
		t.Errorf("snapshot object doesn't look like a commit: %s", out)
	}
}

func TestSnapshotGitRef_notGitStash(t *testing.T) {
	m, _, ws := newTestManagerWithRepo(t)
	ctx := context.Background()

	if err := os.WriteFile(filepath.Join(ws.Dir, "file.txt"), []byte("v2-uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := m.SnapshotGitRef(ctx, ws, 1); err != nil {
		t.Fatalf("SnapshotGitRef: %v", err)
	}

	// The working file must still hold the uncommitted edit — a real
	// `git stash` would have reverted it to HEAD's v1.
	got, err := os.ReadFile(filepath.Join(ws.Dir, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "v2-uncommitted" {
		t.Errorf("working tree file = %q, snapshot touched it like git stash would have", got)
	}
}

func TestSnapshotTree_realCloneAndForcedFallbackBothProduceRestoreableCopies(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "b.txt"), []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("natural", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "dst")
		kind, path, err := workspace.SnapshotTree(src, dest, false)
		if err != nil {
			t.Fatalf("SnapshotTree: %v", err)
		}
		t.Logf("natural snapshot kind=%s path=%s", kind, path)
		assertRestoredContent(t, kind, path, dest)
	})

	t.Run("forced fallback", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "dst")
		kind, path, err := workspace.SnapshotTree(src, dest, true)
		if err != nil {
			t.Fatalf("SnapshotTree: %v", err)
		}
		if kind != "tar.zst" {
			t.Fatalf("kind = %q, want tar.zst (forced fallback path)", kind)
		}
		assertRestoredContent(t, kind, path, dest)
	})
}

func assertRestoredContent(t *testing.T, kind, path, cowDest string) {
	t.Helper()
	if kind == "cow" {
		got, err := os.ReadFile(filepath.Join(cowDest, "a.txt"))
		if err != nil {
			t.Fatalf("reading cloned a.txt: %v", err)
		}
		if string(got) != "hello" {
			t.Errorf("a.txt = %q, want hello", got)
		}
		got, err = os.ReadFile(filepath.Join(cowDest, "sub", "b.txt"))
		if err != nil {
			t.Fatalf("reading cloned sub/b.txt: %v", err)
		}
		if string(got) != "world" {
			t.Errorf("sub/b.txt = %q, want world", got)
		}
		return
	}
	// tar.zst path: unpack and check.
	extractDir := path + ".extracted"
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("tar", "--zstd", "-xf", path, "-C", extractDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("extracting tar.zst: %v: %s", err, out)
	}
	got, err := os.ReadFile(filepath.Join(extractDir, "a.txt"))
	if err != nil {
		t.Fatalf("reading extracted a.txt: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("a.txt = %q, want hello", got)
	}
}

func gitStatus(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain").CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v: %s", err, out)
	}
	return string(out)
}
