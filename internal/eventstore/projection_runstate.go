package eventstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/events"
)

// RunStateProjection folds a run's stream through domain.Advance itself —
// "state is a projection" (L2) means exactly this, and L01-domain-model.md
// documents this as what L02's Projection.Apply calls under the hood — and
// upserts the resulting RunState as a JSON blob keyed by run_id, so a
// caller can read current state without re-folding a whole run's history
// on every read (06-durability.md: "read-your-writes").
//
// now for the fold is env.OccurredAt, never time.Now() — this is what
// keeps Reset+replay deterministic (AGENTS §4 rule 4 applied at the
// projection layer).
type RunStateProjection struct{}

func (RunStateProjection) Name() string { return "runstate" }
func (RunStateProjection) Version() int { return 1 }

func (RunStateProjection) Reset(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM run_state_projection`)
	return err
}

func (RunStateProjection) Apply(ctx context.Context, tx *sql.Tx, env events.Envelope) error {
	if IsAuxStream(env.StreamID) {
		// Non-run-scoped facts (L05's engine.*/process.orphan.reaped, L14's
		// conversation.message.appended) — domain.Advance has no case for
		// them and none is needed; see IsAuxStream's doc comment.
		return nil
	}
	state, err := loadRunState(ctx, tx, env.StreamID)
	if err != nil {
		return err
	}

	next, _, err := domain.Advance(state, env.Event, env.OccurredAt)
	if err != nil {
		return fmt.Errorf("folding %s for run %s: %w", env.EventType, env.StreamID, err)
	}

	stateJSON, err := json.Marshal(next)
	if err != nil {
		return fmt.Errorf("marshalling run state: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO run_state_projection (run_id, state_json, status, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(run_id) DO UPDATE SET
			state_json = excluded.state_json,
			status     = excluded.status,
			updated_at = excluded.updated_at
	`, next.ID, string(stateJSON), string(next.Status), env.RecordedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("upserting run state: %w", err)
	}
	return nil
}

// loadRunState reads the current folded RunState for runID, or a zero
// RunState if none exists yet (the run's first event).
func loadRunState(ctx context.Context, tx *sql.Tx, runID string) (domain.RunState, error) {
	var stateJSON string
	err := tx.QueryRowContext(ctx, `SELECT state_json FROM run_state_projection WHERE run_id = ?`, runID).Scan(&stateJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RunState{}, nil
	}
	if err != nil {
		return domain.RunState{}, fmt.Errorf("reading run state for %s: %w", runID, err)
	}
	var state domain.RunState
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
		return domain.RunState{}, fmt.Errorf("unmarshalling run state for %s: %w", runID, err)
	}
	return state, nil
}
