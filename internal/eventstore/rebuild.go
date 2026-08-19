package eventstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/williamokano/kairos/internal/domain"
)

// verifyProjections runs at Store Open: for every registered projection,
// if its recorded schema_version in projection_offsets is absent or below
// Projection.Version(), it Resets and replays the whole log through it —
// "a projection Version() bump triggers automatic rebuild at boot"
// (01-architecture.md).
func (s *store) verifyProjections(ctx context.Context) error {
	tx, err := s.writerDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning verify transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, p := range s.projs {
		var version int
		var lastSeq int64
		err := tx.QueryRowContext(ctx, `SELECT version, last_global_seq FROM projection_offsets WHERE name = ?`, p.Name()).Scan(&version, &lastSeq)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if err := s.rebuildOne(ctx, tx, p); err != nil {
				return err
			}
		case err != nil:
			return fmt.Errorf("reading projection_offsets for %s: %w", p.Name(), err)
		case version < p.Version():
			if err := s.rebuildOne(ctx, tx, p); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// rebuildOne Resets p and replays every event in the log through it, in
// global_seq order, recording the new version and offset. Events are
// buffered into memory before replay so p.Apply's own queries never run
// against the same connection while an outer *sql.Rows cursor is open —
// SQLite does not support nested statements safely per connection.
func (s *store) rebuildOne(ctx context.Context, tx *sql.Tx, p Projection) error {
	if err := p.Reset(ctx, tx); err != nil {
		return fmt.Errorf("resetting projection %s: %w", p.Name(), err)
	}

	rows, err := tx.QueryContext(ctx, `SELECT `+eventColumns+` FROM events ORDER BY global_seq ASC`)
	if err != nil {
		return fmt.Errorf("querying events for rebuild of %s: %w", p.Name(), err)
	}
	envs, err := s.scanAll(rows)
	_ = rows.Close()
	if err != nil {
		return fmt.Errorf("scanning events for rebuild of %s: %w", p.Name(), err)
	}

	var maxSeq int64
	for _, env := range envs {
		if err := p.Apply(ctx, tx, env); err != nil {
			return fmt.Errorf("replaying %s at global_seq %d into %s: %w", env.EventType, env.GlobalSeq, p.Name(), err)
		}
		maxSeq = env.GlobalSeq
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO projection_offsets (name, version, last_global_seq)
		VALUES (?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET version = excluded.version, last_global_seq = excluded.last_global_seq
	`, p.Name(), p.Version(), maxSeq)
	if err != nil {
		return fmt.Errorf("recording projection_offsets for %s: %w", p.Name(), err)
	}
	return nil
}

// Rebuild forces every registered projection to Reset and replay,
// regardless of its recorded version — the library primitive behind a
// future `kairos db reindex` (deferred to L04; see L02-event-store.md).
func (s *store) Rebuild(ctx context.Context) error {
	tx, err := s.writerDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning rebuild transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, p := range s.projs {
		if err := s.rebuildOne(ctx, tx, p); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// VerifyReport is Verify's result: any run whose replayed projection
// diverges from what is persisted.
type VerifyReport struct {
	MismatchedRunIDs []string
}

// Verify replays every stream from scratch (in memory, not touching the
// persisted projection tables) and diffs the result against what
// RunStateProjection has stored — the library primitive behind a future
// `kairos db verify` (deferred to L04).
func (s *store) Verify(ctx context.Context) (VerifyReport, error) {
	rows, err := s.readerDB.QueryContext(ctx, `SELECT `+eventColumns+` FROM events ORDER BY global_seq ASC`)
	if err != nil {
		return VerifyReport{}, fmt.Errorf("querying events for verify: %w", err)
	}
	envs, err := s.scanAll(rows)
	_ = rows.Close()
	if err != nil {
		return VerifyReport{}, fmt.Errorf("scanning events for verify: %w", err)
	}

	scratch := map[string]domain.RunState{} // run_id -> folded state, in memory only
	for _, env := range envs {
		next, _, err := domain.Advance(scratch[env.StreamID], env.Event, env.OccurredAt)
		if err != nil {
			return VerifyReport{}, fmt.Errorf("replaying %s at global_seq %d: %w", env.EventType, env.GlobalSeq, err)
		}
		scratch[env.StreamID] = next
	}

	var mismatches []string
	for runID, wantState := range scratch {
		var gotStatus string
		err := s.readerDB.QueryRowContext(ctx, `SELECT status FROM run_state_projection WHERE run_id = ?`, runID).Scan(&gotStatus)
		if err != nil || gotStatus != string(wantState.Status) {
			mismatches = append(mismatches, runID)
		}
	}
	return VerifyReport{MismatchedRunIDs: mismatches}, nil
}
