package artifact

import (
	"fmt"
	"io"
	"os"

	"github.com/klauspost/compress/zstd"
	"github.com/oklog/ulid/v2"
)

// RotateThreshold is 06-durability.md's log rotation size: "append-only,
// zstd on rotation at 64 MiB."
const RotateThreshold = 64 * 1024 * 1024

// CollectLog zstd-compresses path (a completed node execution's stdout.log
// or stderr.log) into path+".zst" and removes the original, if and only
// if path is at least RotateThreshold bytes — a small log is left exactly
// as-is. It reports whether it rotated.
//
// This runs once, at node-execution completion (called from the engine's
// reapShell/reapLLM), never while the child holds the file open for
// writing: the child's fd is a real file, not a pipe, because process
// adoption's durability guarantee depends on that (06-durability.md,
// "pipes die with their reader") — truncating or renaming a log out from
// under a live writer's fd would corrupt the stream mid-write. Rotating a
// log that is still growing past 64 MiB during a single very long node
// execution is therefore explicitly out of scope here; see
// L09-artifacts-logs.md's Future work.
//
// Compression is written to a temp file and renamed into place, so a
// failure partway through (e.g. a full disk) leaves the original log
// completely untouched — the caller decides what to record when
// CollectLog returns an error (this package has no event store to append
// to; see internal/engine's collectLogs, which turns a non-nil error here
// into a LogDegraded fact rather than treating it as fatal).
func CollectLog(path string) (rotated bool, err error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("statting %s: %w", path, err)
	}
	if info.Size() < RotateThreshold {
		return false, nil
	}

	src, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("opening %s: %w", path, err)
	}
	defer func() { _ = src.Close() }()

	tmp := path + ".zst.tmp." + ulid.Make().String()
	dst, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return false, fmt.Errorf("creating %s: %w", tmp, err)
	}
	// removeTmp is a no-op once the rename below succeeds (nothing left at
	// tmp to remove); it exists to clean up a partial file on any earlier
	// failure path.
	removeTmp := func() { _ = os.Remove(tmp) }

	w, err := zstd.NewWriter(dst)
	if err != nil {
		_ = dst.Close()
		removeTmp()
		return false, fmt.Errorf("creating zstd writer: %w", err)
	}
	if _, err := io.Copy(w, src); err != nil {
		_ = w.Close()
		_ = dst.Close()
		removeTmp()
		return false, fmt.Errorf("compressing %s: %w", path, err)
	}
	if err := w.Close(); err != nil {
		_ = dst.Close()
		removeTmp()
		return false, fmt.Errorf("flushing zstd writer: %w", err)
	}
	if err := dst.Close(); err != nil {
		removeTmp()
		return false, fmt.Errorf("closing %s: %w", tmp, err)
	}

	final := path + ".zst"
	if err := os.Rename(tmp, final); err != nil {
		removeTmp()
		return false, fmt.Errorf("renaming %s to %s: %w", tmp, final, err)
	}
	if err := os.Remove(path); err != nil {
		// The compressed copy already exists and is valid; losing the
		// race to remove the original is a leftover, not data loss — but
		// it is still reported rather than swallowed (AGENTS §4 rule 1).
		return true, fmt.Errorf("removing original %s after compression: %w", path, err)
	}
	return true, nil
}
