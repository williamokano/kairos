package exectest

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/williamokano/kairos/internal/executor/local"
)

// Fake is an in-memory local.Executor for internal/engine's unit tests
// that exercise dispatch routing, retry/loop-guard paths, and cmd-to-cmd
// mapping without needing a real subprocess.
type Fake struct {
	mu       sync.Mutex
	nextPID  int
	started  []local.ExecSpec
	signals  []FakeSignal
	exitCode map[int]int // pid -> exit code to report from Wait
	waitCh   map[int]chan struct{}
}

// FakeSignal records one Signal call, for assertions.
type FakeSignal struct {
	PGID int
	Sig  local.Signal
}

// NewFake returns an empty Fake ready to use.
func NewFake() *Fake {
	return &Fake{
		nextPID:  1,
		exitCode: make(map[int]int),
		waitCh:   make(map[int]chan struct{}),
	}
}

func (f *Fake) Start(ctx context.Context, spec local.ExecSpec) (local.Started, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pid := f.nextPID
	f.nextPID++
	f.started = append(f.started, spec)
	f.waitCh[pid] = make(chan struct{})
	f.exitCode[pid] = 0
	return local.Started{PID: pid, PGID: pid, Nonce: fmt.Sprintf("fake-nonce-%d", pid), StartedAt: time.Now().UTC(), Dir: spec.Dir}, nil
}

// Finish marks pid as exited with the given code, unblocking any Wait call.
func (f *Fake) Finish(pid, exitCode int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exitCode[pid] = exitCode
	if ch, ok := f.waitCh[pid]; ok {
		close(ch)
	}
}

func (f *Fake) Wait(ctx context.Context, pid int) (local.ExitResult, error) {
	f.mu.Lock()
	ch, ok := f.waitCh[pid]
	f.mu.Unlock()
	if !ok {
		return local.ExitResult{}, fmt.Errorf("fake: no tracked process for pid %d", pid)
	}
	select {
	case <-ch:
	case <-ctx.Done():
		return local.ExitResult{}, ctx.Err()
	}
	f.mu.Lock()
	code := f.exitCode[pid]
	f.mu.Unlock()
	return local.ExitResult{ExitCode: code}, nil
}

func (f *Fake) Signal(ctx context.Context, pgid int, sig local.Signal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signals = append(f.signals, FakeSignal{PGID: pgid, Sig: sig})
	return nil
}

// Cancel records a Term-then-Kill pair immediately (no real grace-period
// wait — this is a fake for unit tests, not a timing simulation).
func (f *Fake) Cancel(ctx context.Context, pgid int, killGrace time.Duration) error {
	if err := f.Signal(ctx, pgid, local.SignalTerm); err != nil {
		return err
	}
	return f.Signal(ctx, pgid, local.SignalKill)
}

// Started returns every ExecSpec passed to Start, in order.
func (f *Fake) Started() []local.ExecSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]local.ExecSpec(nil), f.started...)
}

// Signals returns every Signal call received, in order.
func (f *Fake) Signals() []FakeSignal {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]FakeSignal(nil), f.signals...)
}

var _ local.Executor = (*Fake)(nil)
