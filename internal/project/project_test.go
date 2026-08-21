package project_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/williamokano/kairos/internal/events"
	"github.com/williamokano/kairos/internal/eventstore"
	"github.com/williamokano/kairos/internal/executor/local"
	"github.com/williamokano/kairos/internal/project"
)

func openTestStore(t *testing.T) eventstore.Store {
	t.Helper()
	registry, err := events.Builtin()
	if err != nil {
		t.Fatalf("Builtin: %v", err)
	}
	st, err := eventstore.Open(context.Background(), eventstore.Config{
		Path:     filepath.Join(t.TempDir(), "kairos.db"),
		Registry: registry,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func newTestRepo(t *testing.T) string {
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

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out.String())
	}
	return out.String()
}

func newManager(t *testing.T) *project.Manager {
	t.Helper()
	return project.New(openTestStore(t), local.New(local.DefaultBootIDProvider()))
}

func TestCreateProject_detectsGitBackedVsPlain(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()

	gitRepo := newTestRepo(t)
	p, err := m.CreateProject(ctx, "git-proj", gitRepo, "alice")
	if err != nil {
		t.Fatalf("CreateProject (git): %v", err)
	}
	if !p.GitBacked {
		t.Error("expected GitBacked=true for a real git repo")
	}
	if p.CreatedBy != "alice" {
		t.Errorf("CreatedBy = %q, want alice", p.CreatedBy)
	}

	plainDir := t.TempDir()
	p2, err := m.CreateProject(ctx, "plain-proj", plainDir, "bob")
	if err != nil {
		t.Fatalf("CreateProject (plain): %v", err)
	}
	if p2.GitBacked {
		t.Error("expected GitBacked=false for a directory with no .git")
	}
}

func TestCreateProject_persistsAcrossRestart(t *testing.T) {
	registry, err := events.Builtin()
	if err != nil {
		t.Fatalf("Builtin: %v", err)
	}
	dbPath := filepath.Join(t.TempDir(), "kairos.db")
	st1, err := eventstore.Open(context.Background(), eventstore.Config{Path: dbPath, Registry: registry})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	m1 := project.New(st1, local.New(local.DefaultBootIDProvider()))
	p, err := m1.CreateProject(context.Background(), "durable-proj", t.TempDir(), "")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := st1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	st2, err := eventstore.Open(context.Background(), eventstore.Config{Path: dbPath, Registry: registry})
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	t.Cleanup(func() { _ = st2.Close() })
	m2 := project.New(st2, local.New(local.DefaultBootIDProvider()))

	got, ok, err := m2.GetProjectByName(context.Background(), "durable-proj")
	if err != nil {
		t.Fatalf("GetProjectByName after restart: %v", err)
	}
	if !ok {
		t.Fatal("project not found after a simulated restart")
	}
	if got.ID != p.ID {
		t.Errorf("ID = %s, want %s", got.ID, p.ID)
	}
}

func TestStartSession_gitBackedProjectGetsARealIsolatedWorktree(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()
	repo := newTestRepo(t)

	p, err := m.CreateProject(ctx, "wt-proj", repo, "")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	sess, err := m.StartSession(ctx, p.ID, "claude", t.TempDir(), "carol")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if sess.Branch == "" {
		t.Fatal("expected a real branch name for a git-backed project's session")
	}
	if sess.WorkDir == repo {
		t.Fatal("session WorkDir must be a real worktree, not the origin repo path itself")
	}
	if _, err := os.Stat(sess.WorkDir); err != nil {
		t.Fatalf("session WorkDir does not exist: %v", err)
	}

	// Real isolation: the worktree is on its own branch, listed by `git
	// worktree list` run from the ORIGIN checkout — proving this is a
	// genuine git worktree, not a copy.
	list := runGit(t, repo, "worktree", "list")
	if !strings.Contains(list, sess.WorkDir) {
		t.Errorf("git worktree list (from origin) does not show the session's worktree:\n%s", list)
	}
	branches := runGit(t, repo, "branch", "--list", sess.Branch)
	if !strings.Contains(branches, sess.Branch) {
		t.Errorf("expected branch %q to exist in the origin repo, branch --list: %q", sess.Branch, branches)
	}

	// A file created in the worktree must NOT appear in the origin
	// checkout's working tree (isolation), even though they share one
	// object database.
	if err := os.WriteFile(filepath.Join(sess.WorkDir, "session-only.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatalf("writing in worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "session-only.txt")); err == nil {
		t.Error("a file written in the session's worktree leaked into the origin checkout")
	}

	if err := m.EndSession(ctx, sess.ID); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	if _, err := os.Stat(sess.WorkDir); err == nil {
		t.Error("expected the worktree directory to be gone after EndSession")
	}
	listAfter := runGit(t, repo, "worktree", "list")
	if strings.Contains(listAfter, sess.WorkDir) {
		t.Errorf("git worktree list still shows the removed worktree:\n%s", listAfter)
	}
}

func TestStartSession_nonGitProjectGetsItsBarePath(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()
	plainDir := t.TempDir()

	p, err := m.CreateProject(ctx, "plain", plainDir, "")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	sess, err := m.StartSession(ctx, p.ID, "claude", t.TempDir(), "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if sess.Branch != "" {
		t.Errorf("expected no branch for a non-git project's session, got %q", sess.Branch)
	}
	if sess.WorkDir != plainDir {
		t.Errorf("WorkDir = %q, want the project's own bare path %q", sess.WorkDir, plainDir)
	}
}

func TestStartSession_noProjectUsesTheGivenScratchDir(t *testing.T) {
	m := newManager(t)
	scratch := t.TempDir()
	sess, err := m.StartSession(context.Background(), "", "claude", scratch, "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if sess.WorkDir != scratch {
		t.Errorf("WorkDir = %q, want the passed-in ad hoc scratch dir %q", sess.WorkDir, scratch)
	}
	if sess.ProjectID != "" {
		t.Errorf("ProjectID = %q, want empty for a project-less session", sess.ProjectID)
	}
}
