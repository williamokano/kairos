package local

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// Signal is a small, syscall-free wrapper around the two signals callers
// outside this package ever need to name. AGENTS.md §2 restricts
// "syscall" itself to internal/executor/local (and its designated test
// helpers) — a plain `Signal(ctx, pgid int, sig syscall.Signal)` parameter
// would force every caller (internal/engine) to import "syscall" just to
// pass SIGTERM/SIGKILL, which is exactly the boundary AGENTS is
// protecting. This type lets a caller reference local.SignalTerm/
// local.SignalKill instead.
type Signal int

const (
	SignalTerm Signal = Signal(0)
	SignalKill Signal = Signal(1)
)

func (s Signal) os() syscall.Signal {
	if s == SignalKill {
		return syscall.SIGKILL
	}
	return syscall.SIGTERM
}

// Executor is the one execution chokepoint's public surface
// (01-architecture.md). Wait and Cancel are pragmatic additions beyond
// the architecture doc's Start/Signal pair: something has to learn when a
// child exits and read its exit code (only this package holds the
// *exec.Cmd handle needed to call cmd.Wait()), and callers need the full
// TERM->grace->KILL cancellation sequence without reimplementing its
// ESRCH-tolerant final step themselves.
type Executor interface {
	Start(ctx context.Context, spec ExecSpec) (Started, error)
	Signal(ctx context.Context, pgid int, sig Signal) error
	Wait(ctx context.Context, pid int) (ExitResult, error)
	Cancel(ctx context.Context, pgid int, killGrace time.Duration) error
}

// ExitResult is what Wait returns once a child exits (or the wait itself
// fails, e.g. because the pid was never tracked here).
type ExitResult struct {
	ExitCode int
	Err      error
}

// Local is the only Executor implementation in this repository. Every
// child process the daemon spawns is born through Start.
type Local struct {
	bootID BootIDProvider

	mu      sync.Mutex
	tracked map[int]*exec.Cmd
}

// New returns a Local executor using boot. Pass DefaultBootIDProvider()
// in production; tests inject a fake to control reconciliation scenarios.
func New(boot BootIDProvider) *Local {
	return &Local{bootID: boot, tracked: make(map[int]*exec.Cmd)}
}

func (l *Local) track(pid int, cmd *exec.Cmd) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.tracked[pid] = cmd
}

func (l *Local) untrack(pid int) *exec.Cmd {
	l.mu.Lock()
	defer l.mu.Unlock()
	cmd := l.tracked[pid]
	delete(l.tracked, pid)
	return cmd
}

// Wait blocks until the process started with the given pid exits, then
// returns its exit code. It is only valid to call once per pid returned
// by Start — the underlying *exec.Cmd is untracked on return.
func (l *Local) Wait(ctx context.Context, pid int) (ExitResult, error) {
	l.mu.Lock()
	cmd := l.tracked[pid]
	l.mu.Unlock()
	if cmd == nil {
		return ExitResult{}, fmt.Errorf("local: no tracked process for pid %d", pid)
	}

	err := cmd.Wait()
	l.untrack(pid)

	if err == nil {
		return ExitResult{ExitCode: 0}, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return ExitResult{ExitCode: exitErr.ExitCode()}, nil
	}
	return ExitResult{Err: err}, nil
}

// Signal sends sig to the process group led by pgid — never a bare pid,
// since a negative pgid targets the whole group in one syscall.
func (l *Local) Signal(ctx context.Context, pgid int, sig Signal) error {
	if pgid <= 0 {
		return fmt.Errorf("local: refusing to signal pgid %d", pgid)
	}
	osSig := sig.os()
	if err := syscall.Kill(-pgid, osSig); err != nil {
		return fmt.Errorf("signalling pgid %d with %s: %w", pgid, osSig, err)
	}
	return nil
}
