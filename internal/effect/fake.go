package effect

import (
	"context"
	"sync"
)

// Fake is an in-memory Provider double for engine/reconcile tests — never
// a real network call, matching L08's established fake-CLI-stub
// discipline for anything that would otherwise touch the outside world.
type Fake struct {
	mu sync.Mutex

	kind string

	// AttemptResult is returned by every Attempt call unless AttemptErr
	// is set. AttemptFunc, if set, overrides both for per-call control
	// (e.g. failing the first attempt, succeeding the retry).
	AttemptResult Result
	AttemptErr    error
	AttemptFunc   func(Request) (Result, error)

	ProbeResult Result
	ProbeOK     bool
	ProbeErr    error

	CompensateErr error

	AttemptCalls    []Request
	ProbeCalls      []Request
	CompensateCalls []string // externalRefs
}

// NewFake returns a Fake answering for kind.
func NewFake(kind string) *Fake {
	return &Fake{kind: kind}
}

func (f *Fake) Kind() string { return f.kind }

func (f *Fake) Attempt(_ context.Context, req Request) (Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.AttemptCalls = append(f.AttemptCalls, req)
	if f.AttemptFunc != nil {
		return f.AttemptFunc(req)
	}
	return f.AttemptResult, f.AttemptErr
}

func (f *Fake) Probe(_ context.Context, req Request) (Result, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ProbeCalls = append(f.ProbeCalls, req)
	return f.ProbeResult, f.ProbeOK, f.ProbeErr
}

func (f *Fake) Compensate(_ context.Context, _ Request, externalRef string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.CompensateCalls = append(f.CompensateCalls, externalRef)
	return f.CompensateErr
}

// CallCount reports how many times Attempt has been called — tests use
// this to assert a dry-run or a denied confirmation never reached the
// provider at all.
func (f *Fake) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.AttemptCalls)
}

// Compensations returns a snapshot of every externalRef Compensate has
// been called with — the thread-safe way to read CompensateCalls;
// reading the field directly races compensateRun's own background
// goroutine (shard.go dispatches it via e.wg.Add/go func).
func (f *Fake) Compensations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.CompensateCalls))
	copy(out, f.CompensateCalls)
	return out
}
