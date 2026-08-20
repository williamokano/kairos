// Package workspace implements ADR 0005's per-run workspace scheme: a
// bare, gc-disabled mirror per source repo, borrowed from by a private
// clone per run via git's object alternates
// (adr/0005-reference-clone-per-run.md). Every git invocation goes
// through the one execution chokepoint (internal/executor/local), never
// os/exec directly — AGENTS.md §2's "internal/workspace runs git through
// the executor, not through exec.Command" rule.
package workspace

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/williamokano/kairos/internal/executor/local"
)

// Manager provisions and reclaims per-run workspaces under workRoot,
// backed by per-repo mirrors under mirrorRoot.
type Manager struct {
	mirrorRoot string
	workRoot   string
	exec       local.Executor
}

// New returns a Manager. exec is the same local.Executor the engine
// already uses for node processes — git clones are just another tracked
// child process, born through the same chokepoint.
func New(mirrorRoot, workRoot string, exec local.Executor) *Manager {
	return &Manager{mirrorRoot: mirrorRoot, workRoot: workRoot, exec: exec}
}

// Workspace is one run's provisioned repo clone.
type Workspace struct {
	RunID string
	Dir   string // workRoot/<runID>/repo
}

func (m *Manager) runDir(runID string) string {
	return filepath.Join(m.workRoot, runID, "repo")
}

// Provision clones the repository containing sourceRepo into a per-run
// workspace, borrowing objects via --reference against a locally
// maintained bare mirror (created on first use). If runID's workspace
// already exists, it is returned as-is — Provision is idempotent so a
// retry after a crash between mirror creation and workspace clone does
// not redo work it already finished.
func (m *Manager) Provision(ctx context.Context, runID, sourceRepo string) (Workspace, error) {
	dir := m.runDir(runID)
	if Verify(dir) {
		return Workspace{RunID: runID, Dir: dir}, nil
	}

	repoRoot, err := findRepoRoot(sourceRepo)
	if err != nil {
		return Workspace{}, err
	}
	if err := m.checkSafeSource(repoRoot); err != nil {
		return Workspace{}, err
	}

	mirrorPath, err := m.ensureMirror(ctx, repoRoot)
	if err != nil {
		return Workspace{}, err
	}

	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return Workspace{}, fmt.Errorf("creating run work dir: %w", err)
	}
	if err := m.runGit(ctx, "", "clone", "--reference", mirrorPath, mirrorPath, dir); err != nil {
		return Workspace{}, fmt.Errorf("provisioning workspace for run %s: %w", runID, err)
	}
	return Workspace{RunID: runID, Dir: dir}, nil
}

// Reprovision destroys and recreates runID's workspace from scratch — the
// explicit, caller-confirmed "this workspace is corrupt" path
// 06-durability.md names (workspace.corrupt -> re-provision). It is never
// called automatically by reconciliation: a dirty tree may be the only
// copy of an agent's uncommitted work, so deleting one is a decision only
// a caller who has independently verified corruption (Verify returning
// false is not, by itself, proof of corruption — it may simply mean
// "never provisioned yet") gets to make.
func (m *Manager) Reprovision(ctx context.Context, runID, sourceRepo string) (Workspace, error) {
	if err := os.RemoveAll(m.runDir(runID)); err != nil {
		return Workspace{}, fmt.Errorf("removing corrupt workspace: %w", err)
	}
	return m.Provision(ctx, runID, sourceRepo)
}

// Verify reports whether dir looks like an intact git workspace — cheap,
// no subprocess, safe to call from a hot path.
func Verify(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// GC removes every runID directory under workRoot that is not named in
// activeRunIDs — the disposal half of workspace lifecycle. Never wired to
// a timer (no timer wheel exists yet, matching L05-engine.md's decision
// #6 precedent): a caller invokes this explicitly, e.g. from a `kairos db
// gc` verb or a future scheduled maintenance pass.
func (m *Manager) GC(ctx context.Context, activeRunIDs map[string]bool) ([]string, error) {
	entries, err := os.ReadDir(m.workRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading work root: %w", err)
	}
	var removed []string
	for _, e := range entries {
		if !e.IsDir() || activeRunIDs[e.Name()] {
			continue
		}
		full := filepath.Join(m.workRoot, e.Name())
		if err := os.RemoveAll(full); err != nil {
			return removed, fmt.Errorf("removing %s: %w", full, err)
		}
		removed = append(removed, e.Name())
	}
	return removed, nil
}

// findRepoRoot walks up from start looking for a .git entry — no
// subprocess needed for this half of "the repo containing cwd".
func findRepoRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("resolving %q: %w", start, err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no .git directory found above %q", start)
		}
		dir = parent
	}
}

// checkSafeSource enforces ADR 0005 property 3's hard refusals: an
// autonomous agent must never be pointed at $HOME, the filesystem root,
// or Kairos's own state directory. (The ADR's third refusal — "your own
// checkout with uncommitted changes" — needs a `git status --porcelain`
// round trip this document does not add; see L06-workspaces.md's Future
// work.)
func (m *Manager) checkSafeSource(repoRoot string) error {
	if repoRoot == string(filepath.Separator) {
		return fmt.Errorf("refusing to use %q as a workspace source: filesystem root", repoRoot)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && repoRoot == home {
		return fmt.Errorf("refusing to use %q as a workspace source: your home directory", repoRoot)
	}
	kairosHome, err := filepath.Abs(filepath.Dir(m.workRoot))
	if err == nil {
		sep := string(filepath.Separator)
		if repoRoot == kairosHome || strings.HasPrefix(repoRoot+sep, kairosHome+sep) {
			return fmt.Errorf("refusing to use %q as a workspace source: inside kairos's own state directory", repoRoot)
		}
	}
	return nil
}

// mirrorKeyFor derives a stable, filesystem-safe mirror path for
// repoRoot. Real host/owner/repo-keyed mirrors (so two clones of the same
// remote share one mirror) are Future work — nothing in this document's
// scope has a remote URL to key on yet; see L06-workspaces.md.
func mirrorKeyFor(repoRoot string) string {
	sum := sha1.Sum([]byte(repoRoot))
	return filepath.Join("local", fmt.Sprintf("%s-%s.git", filepath.Base(repoRoot), hex.EncodeToString(sum[:])[:12]))
}

// ensureMirror creates repoRoot's bare mirror on first use (gc.auto=0,
// gc.pruneExpire=never per ADR 0005's "sharp edge" warning) or reuses an
// existing one as-is — refreshing a stale mirror via `git fetch` is
// Future work, not needed for a single-clone-per-process-lifetime test
// scenario.
func (m *Manager) ensureMirror(ctx context.Context, repoRoot string) (string, error) {
	mirrorPath := filepath.Join(m.mirrorRoot, mirrorKeyFor(repoRoot))
	if _, err := os.Stat(mirrorPath); err == nil {
		return mirrorPath, nil
	}
	if err := os.MkdirAll(filepath.Dir(mirrorPath), 0o700); err != nil {
		return "", fmt.Errorf("creating mirror parent dir: %w", err)
	}
	if err := m.runGit(ctx, "", "clone", "--mirror", repoRoot, mirrorPath); err != nil {
		return "", fmt.Errorf("mirroring %q: %w", repoRoot, err)
	}
	if err := m.runGit(ctx, mirrorPath, "config", "gc.auto", "0"); err != nil {
		return "", err
	}
	if err := m.runGit(ctx, mirrorPath, "config", "gc.pruneExpire", "never"); err != nil {
		return "", err
	}
	return mirrorPath, nil
}

// runGit spawns git as a tracked child process via the executor and
// blocks for its exit — the one path every git invocation in this
// package takes.
func (m *Manager) runGit(ctx context.Context, workDir string, args ...string) error {
	scratchDir, err := os.MkdirTemp("", "kairos-workspace-git-*")
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
