// Package project implements Projects and Sessions — a real, deliberate
// extension the user asked for directly after live-testing `kairos do`:
// a Project names a real working directory; a Session is a stable,
// resumable chat identity bound to a Project (or none).
//
// Sessions use REAL git worktrees, not internal/workspace's `--reference`
// clones — a deliberate reversal of ADR 0005 for this one use case, made
// explicitly by the user after being shown ADR 0005's exact tradeoff.
// See adr/0014-worktrees-for-interactive-sessions.md for the full
// reasoning and the honest residual risk. Every git invocation still
// goes through the one execution chokepoint (internal/executor/local),
// same as internal/workspace — this package does not bypass that rule
// just because its git usage differs from workspace's.
package project

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/oklog/ulid/v2"

	"github.com/williamokano/kairos/internal/eventstore"
	"github.com/williamokano/kairos/internal/executor/local"
)

// Manager creates/lists Projects and Sessions and provisions/reclaims a
// Session's git worktree when its Project is git-backed.
type Manager struct {
	store eventstore.Store
	exec  local.Executor
}

func New(store eventstore.Store, exec local.Executor) *Manager {
	return &Manager{store: store, exec: exec}
}

// CreateProject registers repoPath under name, auto-detecting whether
// it's git-backed (repoPath/.git exists) — per the user's own framing,
// "if it's a git folder, we should be able to start a worktree"; a
// non-git path just gets sessions with a plain, shared CWD and no git
// machinery at all.
func (m *Manager) CreateProject(ctx context.Context, name, repoPath, createdBy string) (eventstore.Project, error) {
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return eventstore.Project{}, fmt.Errorf("project: resolving path %s: %w", repoPath, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return eventstore.Project{}, fmt.Errorf("project: path does not exist: %w", err)
	}
	if !info.IsDir() {
		return eventstore.Project{}, fmt.Errorf("project: %s is not a directory", abs)
	}
	gitBacked := isGitBacked(abs)

	p := eventstore.Project{
		ID: "prj_" + ulid.Make().String(), Name: name, RepoPath: abs,
		GitBacked: gitBacked, CreatedBy: createdBy,
	}
	if err := m.store.UpsertProject(ctx, p); err != nil {
		return eventstore.Project{}, err
	}
	return p, nil
}

func isGitBacked(path string) bool {
	info, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil && (info.IsDir() || info.Mode().IsRegular()) // a worktree's own .git is a file, a real clone's is a dir
}

func (m *Manager) ListProjects(ctx context.Context) ([]eventstore.Project, error) {
	return m.store.ListProjects(ctx)
}

func (m *Manager) GetProjectByName(ctx context.Context, name string) (eventstore.Project, bool, error) {
	return m.store.GetProjectByName(ctx, name)
}

// StartSession creates a Session. If projectID is non-empty and the
// Project is git-backed, a real worktree is provisioned immediately on
// its own branch (kairos/session/<id>) — see provisionWorktree. If the
// Project is not git-backed, the Session's WorkDir is just the Project's
// RepoPath directly (no isolation, shared like any other directory two
// terminals could both cd into — a deliberate, honest simplification:
// there is no git machinery to isolate a non-git directory with). If
// projectID is empty, WorkDir is workRoot's own ad hoc scratch
// convention (the caller — internal/api's session handler — passes
// today's existing scratch-dir path, unchanged from before Sessions
// existed).
func (m *Manager) StartSession(ctx context.Context, projectID, actor, adhocScratchDir, createdBy string) (eventstore.Session, error) {
	id := "ses_" + ulid.Make().String()
	workDir := adhocScratchDir
	branch := ""

	if projectID != "" {
		proj, ok, err := m.store.GetProject(ctx, projectID)
		if err != nil {
			return eventstore.Session{}, err
		}
		if !ok {
			return eventstore.Session{}, fmt.Errorf("project: no such project %s", projectID)
		}
		if proj.GitBacked {
			branch = "kairos/session/" + id
			wtDir, err := m.provisionWorktree(ctx, proj.RepoPath, branch)
			if err != nil {
				return eventstore.Session{}, fmt.Errorf("project: provisioning worktree: %w", err)
			}
			workDir = wtDir
		} else {
			workDir = proj.RepoPath
		}
	}

	sess := eventstore.Session{
		ID: id, ProjectID: projectID, Actor: actor, WorkDir: workDir, Branch: branch, CreatedBy: createdBy,
	}
	if err := m.store.UpsertSession(ctx, sess); err != nil {
		return eventstore.Session{}, err
	}
	return sess, nil
}

func (m *Manager) GetSession(ctx context.Context, id string) (eventstore.Session, bool, error) {
	return m.store.GetSession(ctx, id)
}

func (m *Manager) ListSessions(ctx context.Context) ([]eventstore.Session, error) {
	return m.store.ListSessions(ctx)
}

// RecordTurn is called after every `kairos do --session <id>` turn:
// bumps run_count/last_used_at and remembers the native LLM session id
// so the NEXT turn resumes it (mirroring the run-chained --continue
// mechanics, now keyed by the stable Session instead).
func (m *Manager) RecordTurn(ctx context.Context, sessionID, nativeSessionID, runID string) error {
	return m.store.TouchSession(ctx, sessionID, nativeSessionID, runID)
}

// EndSession removes a git-backed session's worktree (if any) and
// deletes its branch — real cleanup, not just a DB row deletion. Safe to
// call on a non-git-backed or ad hoc session (a no-op past the DB
// delete, since there's no worktree to remove).
func (m *Manager) EndSession(ctx context.Context, id string) error {
	sess, ok, err := m.store.GetSession(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if sess.Branch != "" && sess.ProjectID != "" {
		proj, ok, err := m.store.GetProject(ctx, sess.ProjectID)
		if err == nil && ok {
			if err := m.removeWorktree(ctx, proj.RepoPath, sess.WorkDir, sess.Branch); err != nil {
				return fmt.Errorf("project: removing worktree: %w", err)
			}
		}
	}
	return nil
}

// provisionWorktree runs `git worktree add <dir> -b <branch>` off
// repoPath's own real checkout (never a bare mirror — this is the whole
// point of ADR 0014's reversal: a session's worktree IS a first-class
// checkout of the user's real repo, sharing its object store and ref
// namespace on purpose, for the lifetime of one interactive session).
// The worktree lives under repoPath's own parent, at
// <repoPath>-sessions/<branch-suffix>, so `git worktree list` run from
// the user's real checkout shows it plainly.
func (m *Manager) provisionWorktree(ctx context.Context, repoPath, branch string) (string, error) {
	sessionsRoot := repoPath + "-sessions"
	if err := os.MkdirAll(sessionsRoot, 0o700); err != nil {
		return "", fmt.Errorf("creating sessions root: %w", err)
	}
	dir := filepath.Join(sessionsRoot, strings.TrimPrefix(branch, "kairos/session/"))
	if err := m.runGit(ctx, repoPath, "worktree", "add", dir, "-b", branch); err != nil {
		return "", err
	}
	return dir, nil
}

func (m *Manager) removeWorktree(ctx context.Context, repoPath, worktreeDir, branch string) error {
	// --force: a session's worktree may have uncommitted changes at end
	// of chat (that IS the point of the worktree — an agent was working
	// in it) — this is a deliberate, documented data-loss risk the user
	// accepted when choosing worktrees; see ADR 0014's residual-risk
	// section. `kairos session end` is the only caller; it is never
	// invoked automatically.
	if err := m.runGit(ctx, repoPath, "worktree", "remove", "--force", worktreeDir); err != nil {
		return err
	}
	return m.runGit(ctx, repoPath, "branch", "-D", branch)
}

func (m *Manager) runGit(ctx context.Context, workDir string, args ...string) error {
	scratchDir, err := os.MkdirTemp("", "kairos-project-git-*")
	if err != nil {
		return fmt.Errorf("creating git-op scratch dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(scratchDir) }()

	started, err := m.exec.Start(ctx, local.ExecSpec{
		Dir:     scratchDir,
		WorkDir: workDir,
		Argv:    append([]string{"git"}, args...),
	})
	if err != nil {
		return fmt.Errorf("starting git %v: %w", args, err)
	}
	res, err := m.exec.Wait(ctx, started.PID)
	if err != nil {
		return fmt.Errorf("waiting for git %v: %w", args, err)
	}
	if res.Err != nil {
		return fmt.Errorf("git %v: %w", args, res.Err)
	}
	if res.ExitCode != 0 {
		stderr, _ := os.ReadFile(filepath.Join(started.Dir, "stderr.log"))
		return fmt.Errorf("git %v: exit %d: %s", args, res.ExitCode, strings.TrimSpace(string(stderr)))
	}
	return nil
}
