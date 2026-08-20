package local

import "os/exec"

// LookPath resolves name to an absolute path on PATH — the one place
// outside this package that needs binary resolution (05-gates.md's
// command gate: "resolve the binary to its preflight absolute path")
// without importing "os/exec" itself, which AGENTS.md §2 restricts to
// this package alone.
func LookPath(name string) (string, error) {
	return exec.LookPath(name)
}
