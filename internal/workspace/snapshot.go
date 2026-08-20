// Snapshot support: ADR 0006's two layers — a git-ref snapshot (always
// available, always taken) and an optional copy-on-write tree clone
// (detected by probe, used to accelerate/enrich later work but not yet
// consumed by Fork's restore path — see L18-fork-replay-verify.md's
// Future work).
package workspace

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"

	"github.com/williamokano/kairos/internal/executor/local"
)

// Snapshot is one git-ref snapshot's identity, matching
// domain.WorkspaceSnapshotTaken's Ref/SHA fields.
type Snapshot struct {
	Ref string // refs/kairos/runs/<runID>/<seq>
	SHA string
}

// SnapshotGitRef builds an out-of-band commit of ws's current tree state
// and points refs/kairos/runs/<runID>/<atSeq> at it — ADR 0006's layer 1,
// "always available, semantic, cheap." Deliberately not `git stash`,
// which mutates a working tree an agent may be mid-write in: the whole
// sequence runs against a throwaway GIT_INDEX_FILE, touching no
// user-visible state (not HEAD, not the real index, not the branch, not
// the reflog).
func (m *Manager) SnapshotGitRef(ctx context.Context, ws Workspace, atSeq int) (Snapshot, error) {
	idxFile, err := os.CreateTemp("", "kairos-snapshot-index-*")
	if err != nil {
		return Snapshot{}, fmt.Errorf("creating throwaway index file: %w", err)
	}
	idxPath := idxFile.Name()
	_ = idxFile.Close()
	// git treats an existing-but-empty GIT_INDEX_FILE as a corrupt index
	// ("index file smaller than expected"), not as "start fresh" — remove
	// it so the path is merely reserved (unique), and git creates a real
	// fresh index the first time it writes to it.
	if err := os.Remove(idxPath); err != nil {
		return Snapshot{}, fmt.Errorf("clearing throwaway index placeholder: %w", err)
	}
	defer func() { _ = os.Remove(idxPath) }()

	env := []string{"GIT_INDEX_FILE=" + idxPath}

	if err := m.runGitEnv(ctx, ws.Dir, env, "add", "-A"); err != nil {
		return Snapshot{}, fmt.Errorf("staging snapshot tree: %w", err)
	}
	tree, err := m.runGitOutputEnv(ctx, ws.Dir, env, "write-tree")
	if err != nil {
		return Snapshot{}, fmt.Errorf("writing snapshot tree: %w", err)
	}
	tree = strings.TrimSpace(tree)

	// -p HEAD gives the snapshot commit a parent so it is diffable
	// against the run's starting point (06-durability.md: "git diff
	// refs/kairos/runs/A/5 refs/kairos/runs/A/7"). A workspace with no
	// commits yet (freshly initialised, never committed) has no HEAD to
	// parent against — fall back to a parentless commit rather than
	// failing the snapshot outright.
	commitArgs := []string{"commit-tree", tree, "-m", fmt.Sprintf("kairos snapshot @seq-%d", atSeq)}
	if headOK := m.runGitEnv(ctx, ws.Dir, nil, "rev-parse", "--verify", "-q", "HEAD"); headOK == nil {
		commitArgs = []string{"commit-tree", tree, "-p", "HEAD", "-m", fmt.Sprintf("kairos snapshot @seq-%d", atSeq)}
	}
	sha, err := m.runGitOutputEnv(ctx, ws.Dir, env, commitArgs...)
	if err != nil {
		return Snapshot{}, fmt.Errorf("building snapshot commit: %w", err)
	}
	sha = strings.TrimSpace(sha)

	ref := fmt.Sprintf("refs/kairos/runs/%s/%d", ws.RunID, atSeq)
	if err := m.runGitEnv(ctx, ws.Dir, nil, "update-ref", ref, sha); err != nil {
		return Snapshot{}, fmt.Errorf("updating snapshot ref: %w", err)
	}
	return Snapshot{Ref: ref, SHA: sha}, nil
}

// RestoreGitRef brings a NEW run's workspace to exactly the tree state
// snapshot recorded, by fetching the origin run's workspace directory
// (source of the ref) and checking it out. dst must already be a
// Provision'd clone (so it shares the mirror's object store via
// alternates) — this only moves its working tree, it does not create the
// clone.
func (m *Manager) RestoreGitRef(ctx context.Context, dst Workspace, sourceWorkspaceDir string, snap Snapshot) error {
	if err := m.runGitEnv(ctx, dst.Dir, nil, "fetch", sourceWorkspaceDir, snap.Ref); err != nil {
		return fmt.Errorf("fetching snapshot ref from source workspace: %w", err)
	}
	if err := m.runGitEnv(ctx, dst.Dir, nil, "checkout", "--detach", snap.SHA); err != nil {
		return fmt.Errorf("checking out snapshot: %w", err)
	}
	return nil
}

// runGitEnv is runGit, generalised to accept extra environment variables
// (GIT_INDEX_FILE for snapshot's out-of-band index).
func (m *Manager) runGitEnv(ctx context.Context, workDir string, env []string, args ...string) error {
	_, err := m.runGitCapture(ctx, workDir, env, args...)
	return err
}

// runGitOutputEnv is runGitEnv but returns stdout — needed for
// write-tree/commit-tree, which communicate their result on stdout, not
// via exit code alone.
func (m *Manager) runGitOutputEnv(ctx context.Context, workDir string, env []string, args ...string) (string, error) {
	return m.runGitCapture(ctx, workDir, env, args...)
}

func (m *Manager) runGitCapture(ctx context.Context, workDir string, env []string, args ...string) (string, error) {
	scratchDir, err := os.MkdirTemp("", "kairos-workspace-git-*")
	if err != nil {
		return "", fmt.Errorf("creating git-op scratch dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(scratchDir) }()

	started, err := m.exec.Start(ctx, local.ExecSpec{
		Dir:     scratchDir,
		WorkDir: workDir,
		Env:     env,
		Argv:    append([]string{"git"}, args...),
	})
	if err != nil {
		return "", fmt.Errorf("starting git %v: %w", args, err)
	}
	res, err := m.exec.Wait(ctx, started.PID)
	if err != nil {
		return "", fmt.Errorf("waiting for git %v: %w", args, err)
	}
	stdout, _ := os.ReadFile(filepath.Join(started.Dir, "stdout.log"))
	if res.Err != nil {
		return "", fmt.Errorf("git %v: %w", args, res.Err)
	}
	if res.ExitCode != 0 {
		stderr, _ := os.ReadFile(filepath.Join(started.Dir, "stderr.log"))
		return "", fmt.Errorf("git %v: exit %d: %s", args, res.ExitCode, strings.TrimSpace(string(stderr)))
	}
	return string(stdout), nil
}

// SnapshotTree captures srcDir's full tree (including gitignored build
// state a git-ref snapshot cannot see) into destDir — ADR 0006's layer 2.
// Uses a real reflink clone when ProbeCoWSupport(srcDir) says the
// filesystem supports it (near-instant, metadata-only); falls back to a
// tar.zst archive at destDir+".tar.zst" otherwise. forceFallback exists
// only so tests can exercise the fallback path on a host that DOES
// support CoW, keeping it from being dead code.
func SnapshotTree(srcDir, destDir string, forceFallback bool) (kind string, path string, err error) {
	if !forceFallback && local.ProbeCoWSupport(filepath.Dir(srcDir)) {
		if err := local.CloneTree(srcDir, destDir); err == nil {
			return "cow", destDir, nil
		}
		// A probe can pass yet a specific clone still fail (e.g. crossing
		// a bind mount) — fall through to tar.zst rather than losing the
		// snapshot.
	}
	archivePath := destDir + ".tar.zst"
	if err := tarZstTree(srcDir, archivePath); err != nil {
		return "", "", fmt.Errorf("tar.zst fallback snapshot: %w", err)
	}
	return "tar.zst", archivePath, nil
}

func tarZstTree(srcDir, archivePath string) error {
	f, err := os.Create(archivePath)
	if err != nil {
		return fmt.Errorf("creating archive: %w", err)
	}
	defer func() { _ = f.Close() }()

	zw, err := zstd.NewWriter(f)
	if err != nil {
		return fmt.Errorf("creating zstd writer: %w", err)
	}
	defer func() { _ = zw.Close() }()

	tw := tar.NewWriter(zw)
	defer func() { _ = tw.Close() }()

	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = rel
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = src.Close() }()
		_, err = io.Copy(tw, src)
		return err
	})
}
