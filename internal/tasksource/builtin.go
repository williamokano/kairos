package tasksource

import (
	"context"
	"fmt"
	"sync"
)

// Fake is an in-memory Source for tests — the "you do NOT need real
// GitHub/Jira/Linear integration" stand-in this document's own scope
// note calls for. Every method is driven by fields a test sets directly;
// nothing here talks to a network.
type Fake struct {
	DescribeFn func(ctx context.Context) (Descriptor, error)
	PollFn     func(ctx context.Context, in PollInput) (PollOutput, error)
	AckFn      func(ctx context.Context, in AckInput) (AckOutput, error)

	mu    sync.Mutex
	Acks  []AckInput
	Polls []PollInput
}

func (f *Fake) Describe(ctx context.Context) (Descriptor, error) {
	if f.DescribeFn != nil {
		return f.DescribeFn(ctx)
	}
	return Descriptor{Name: "fake", Kinds: []string{"tasksource"}, Ops: []string{"describe", "poll", "ack"}}, nil
}

func (f *Fake) Poll(ctx context.Context, in PollInput) (PollOutput, error) {
	f.mu.Lock()
	f.Polls = append(f.Polls, in)
	f.mu.Unlock()
	if f.PollFn != nil {
		return f.PollFn(ctx, in)
	}
	return PollOutput{Cursor: in.Cursor}, nil
}

func (f *Fake) Ack(ctx context.Context, in AckInput) (AckOutput, error) {
	f.mu.Lock()
	f.Acks = append(f.Acks, in)
	f.mu.Unlock()
	if f.AckFn != nil {
		return f.AckFn(ctx, in)
	}
	return AckOutput{}, nil
}

// AckCount returns how many times Ack actually ran (as opposed to being
// deduped by ack.go's idempotency check) — tests use this to assert
// "acked exactly once" rather than reaching into ack.go's internals.
func (f *Fake) AckCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Acks)
}

// Constructor builds a Source from a source row's persisted config.
type Constructor func(config []byte) (Source, error)

// Registry maps a source `kind` to its constructor. 08-triggers.md names
// eight compiled-in builtins (github, jira, linear, inbox, cron,
// repo-watch, git, shell); inbox/cron are their own subsystems in this
// package (inbox.go/cron.go) rather than Source implementations, and
// real github/jira/linear API clients are this document's largest
// explicitly-deferred item (see L16-triggers.md's Future work) — the
// registry point exists now specifically so adding them later is a
// one-file change, not a redesign.
type Registry struct {
	mu    sync.Mutex
	ctors map[string]Constructor
}

func NewRegistry() *Registry {
	r := &Registry{ctors: map[string]Constructor{}}
	r.Register("fake", func(config []byte) (Source, error) { return &Fake{}, nil })
	return r
}

func (r *Registry) Register(kind string, ctor Constructor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ctors[kind] = ctor
}

func (r *Registry) Build(kind string, config []byte) (Source, error) {
	r.mu.Lock()
	ctor, ok := r.ctors[kind]
	r.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("tasksource: unknown source kind %q", kind)
	}
	return ctor(config)
}
