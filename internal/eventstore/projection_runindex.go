package eventstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/williamokano/kairos/internal/events"
)

// RunIndexProjection is a narrow (run_id, status, started_at, updated_at)
// index for cheap listing (`kairos ls`) without deserializing a JSON blob
// on every read. It reads the status RunStateProjection already wrote for
// this run in the same transaction rather than folding independently —
// RunStateProjection MUST be registered before RunIndexProjection (see
// store.go's Config.Projections doc) so that row is already current when
// this Apply runs. The value this projection adds is read-time cost, not
// avoiding the fold: the fold happens once, at write time, either way.
type RunIndexProjection struct{}

func (RunIndexProjection) Name() string { return "runindex" }
func (RunIndexProjection) Version() int { return 1 }

func (RunIndexProjection) Reset(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM run_index`)
	return err
}

func (RunIndexProjection) Apply(ctx context.Context, tx *sql.Tx, env events.Envelope) error {
	var status string
	err := tx.QueryRowContext(ctx, `SELECT status FROM run_state_projection WHERE run_id = ?`, env.StreamID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		// RunStateProjection has not run for this envelope (e.g. it is
		// unregistered in this Store's Config) — nothing to index yet.
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading run status for %s: %w", env.StreamID, err)
	}

	updatedAt := env.RecordedAt.Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO run_index (run_id, status, started_at, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(run_id) DO UPDATE SET
			status     = excluded.status,
			started_at = COALESCE(run_index.started_at, excluded.started_at),
			updated_at = excluded.updated_at
	`, env.StreamID, status, updatedAt, updatedAt)
	if err != nil {
		return fmt.Errorf("upserting run index: %w", err)
	}
	return nil
}
