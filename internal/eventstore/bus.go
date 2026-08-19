package eventstore

import (
	"context"
	"sync"

	"github.com/williamokano/kairos/internal/events"
)

// bus is a simple in-process fan-out of post-commit envelopes. Publish is
// called only from the writer goroutine, after commit — never before
// (06-durability.md).
type bus struct {
	mu   sync.Mutex
	subs map[chan events.Envelope]struct{}
}

func newBus() *bus {
	return &bus{subs: make(map[chan events.Envelope]struct{})}
}

func (b *bus) subscribe() (<-chan events.Envelope, func()) {
	ch := make(chan events.Envelope, 64)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	unsub := func() {
		b.mu.Lock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
	return ch, unsub
}

// publish delivers env to every current subscriber, dropping it for a
// subscriber whose channel is full rather than blocking the writer
// goroutine — a slow subscriber must not stall durability.
func (b *bus) publish(env events.Envelope) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- env:
		default:
		}
	}
}

func (s *store) Subscribe(ctx context.Context) (<-chan events.Envelope, func()) {
	return s.bus.subscribe()
}
