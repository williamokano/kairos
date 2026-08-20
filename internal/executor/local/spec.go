// Package local is the one execution chokepoint (L4′): the only package in
// this repository allowed to import os/exec, syscall, or golang.org/x/sys.
// Every child process the daemon ever spawns is born here, in its own
// process group, recorded before fork/exec.
//
// This package's minimal, real slice (Start/Signal, process groups,
// proc.json, cancellation) is L05 scope, not built ahead of it —
// 12-build-plan.md's own milestone tests (TestExecutor_childInOwnProcessGroup,
// the kill-mid-run procedure) require a real subprocess with a real pgid,
// and the mermaid build graph has L06 depending on L05, not the reverse.
// L06 extends these same files (clone workspaces, restartPolicy: adopt,
// reaping polish) rather than recreating them — see L05-engine.md's
// "Documented decisions" for the exact reasoning.
package local

import "time"

// ExecSpec describes one process to start. Dir is where proc.json,
// stdout.log, and stderr.log are written — the execution's own record
// directory. WorkDir is the child's cwd, which may equal Dir for now: L05
// has no workspace/clone machinery (that's L06), so callers pass a bare
// scratch directory.
type ExecSpec struct {
	RunID, NodeID, ExecID string
	Dir                   string
	WorkDir               string
	Env                   []string
	Argv                  []string
	// Stdin, when non-nil, is written to the child's stdin and closed —
	// an llm actor's prompt goes here, never argv (04-agents.md: argv is
	// visible in `ps` to every process on the machine, and prompts
	// routinely contain issue bodies and file excerpts). nil means no
	// stdin, unchanged from every actor kind before L08.
	Stdin []byte
}

// Started is what Start returns once the child is confirmed running.
// Identity is (bootID, PGID, StartedAt) — never a bare PID, which the OS
// reuses (01-architecture.md).
type Started struct {
	PID, PGID int
	Nonce     string
	BootID    string
	StartedAt time.Time
	Dir       string
}
