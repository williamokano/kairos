//go:build linux

package local

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ProbeCoWSupport attempts a real FICLONE ioctl between two throwaway
// files inside dir — ADR 0006: "probe once at startup by attempting a
// clone," never trusted from filesystem type (btrfs/XFS need
// reflink=1 at mkfs time; ext4 never supports it regardless of mount
// options). Lives here, not internal/workspace, because a raw ioctl is
// exactly the class of OS-level operation AGENTS.md's one-execution-
// chokepoint law confines to this package (golang.org/x/sys/unix is on
// TestArchitecture_noExecOutsideExecutor's forbidden-import list
// everywhere else).
func ProbeCoWSupport(dir string) bool {
	src, err := os.CreateTemp(dir, "kairos-cow-probe-src-*")
	if err != nil {
		return false
	}
	srcPath := src.Name()
	defer func() { _ = os.Remove(srcPath) }()
	if _, err := src.WriteString("kairos cow probe"); err != nil {
		_ = src.Close()
		return false
	}
	_ = src.Close()

	dstPath := srcPath + ".dst"
	defer func() { _ = os.Remove(dstPath) }()

	dst, err := os.Create(dstPath)
	if err != nil {
		return false
	}
	defer func() { _ = dst.Close() }()

	srcF, err := os.Open(srcPath)
	if err != nil {
		return false
	}
	defer func() { _ = srcF.Close() }()

	return unix.IoctlFileClone(int(dst.Fd()), int(srcF.Fd())) == nil
}

// CloneTree recursively FICLONEs every regular file from srcDir into
// destDir, preserving directory structure — real per-file reflink, not a
// data copy. Symlinks are recreated as symlinks (never followed and
// cloned as data, which would silently change their meaning).
func CloneTree(srcDir, destDir string) error {
	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(destDir, rel)
		if rel == "." {
			return os.MkdirAll(dst, 0o700)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(target, dst)
		}
		if info.IsDir() {
			return os.MkdirAll(dst, info.Mode().Perm())
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		srcF, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = srcF.Close() }()
		dstF, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		defer func() { _ = dstF.Close() }()
		return unix.IoctlFileClone(int(dstF.Fd()), int(srcF.Fd()))
	})
}
