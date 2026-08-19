package eventstore

import (
	"context"
	"database/sql"

	"github.com/williamokano/kairos/internal/events"
)

// Projection is a pure fold from an Envelope onto SQLite state, applied in
// the same transaction as the append that produced the Envelope
// (06-durability.md: "projection lag is structurally zero"). Apply must
// perform no I/O beyond tx — no network, no clock reads, no randomness —
// so that Reset+replay reproduces exactly the same rows.
//
// Apply takes the full events.Envelope, not a bare domain.Event: a
// projection generally needs StreamID (the run ID) and OccurredAt (for a
// deterministic "now" when folding through domain.Advance), neither of
// which domain.Event itself carries (domain stays pure — see L01).
type Projection interface {
	Name() string
	// Version bumps trigger an automatic Reset+replay at Store Open.
	Version() int
	Apply(ctx context.Context, tx *sql.Tx, env events.Envelope) error
	Reset(ctx context.Context, tx *sql.Tx) error
}
