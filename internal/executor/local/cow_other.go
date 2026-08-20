//go:build !linux

package local

// ProbeCoWSupport always reports unsupported on non-Linux builds — macOS
// clonefile(2) probing is Future work (see L18-fork-replay-verify.md),
// not silently assumed to work. internal/workspace's SnapshotTree falls
// back to tar.zst here, which is honest per ADR 0006's "skipped with a
// recorded reason" rather than a guess.
func ProbeCoWSupport(string) bool { return false }

func CloneTree(string, string) error {
	panic("local: CloneTree called without a supporting ProbeCoWSupport result")
}
