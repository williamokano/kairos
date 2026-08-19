// Package events owns the event *shape*: the store-facing envelope that
// carries audit fields domain.Event itself must never hold (L01's Advance
// stays pure — no I/O, no clock, no randomness), the JSON-Schema registry,
// and the fixture corpus that keeps every shipped event version replayable
// forever (AGENTS.md §4 rule 6). It performs no SQL and knows nothing about
// SQLite; internal/eventstore owns persistence.
package events

import (
	"time"

	"github.com/williamokano/kairos/internal/domain"
)

// Envelope is one durable record: a domain.Event plus the audit metadata
// the event store needs and domain must never carry. Sequence, GlobalSeq,
// and RecordedAt are set by the store on append and are zero on the way in.
type Envelope struct {
	StreamID      string
	Sequence      int   // per-stream, gapless, from 1 — set by the store
	GlobalSeq     int64 // set by the store
	EventType     string
	EventVersion  int
	OccurredAt    time.Time // caller-supplied: when the fact became true
	RecordedAt    time.Time // store-supplied: when it was durably written
	Actor         string    // "engine", "human:<id>", "system"
	CausationSeq  *int64    // global_seq of the event that caused this one, if any
	CorrelationID string
	Event         domain.Event
}
