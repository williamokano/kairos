package eventstore

import (
	"context"
	"database/sql"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/events"
)

// HumanTaskIndexProjection is a real, indexed answer to "what's currently
// waiting on a human" — L20-webui.md's Documented decision #5 named the
// gap: the web home page's "waiting on you" section did a GetRun call
// per non-terminal run (O(active runs) per page load) because no such
// index existed. Both kinds of thing a human resolves via `kairos
// approve` (engine.Approve) are indexed identically: a wait: human task
// (HumanTaskCreated/Answered) and a parked confirm-tier effect
// (EffectConfirmationParked/Answered) — the web page doesn't need to
// know which kind a row is to decide whether to show it, only whether it
// is still open.
type HumanTaskIndexProjection struct{}

func (HumanTaskIndexProjection) Name() string { return "humantasks" }
func (HumanTaskIndexProjection) Version() int { return 1 }

func (HumanTaskIndexProjection) Reset(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM human_task_index`)
	return err
}

func (HumanTaskIndexProjection) Apply(ctx context.Context, tx *sql.Tx, env events.Envelope) error {
	if IsAuxStream(env.StreamID) {
		return nil
	}
	switch ev := env.Event.(type) {
	case domain.HumanTaskCreated:
		return insertHumanTask(ctx, tx, ev.RunID, ev.NodeID, "human", env.OccurredAt)
	case domain.HumanTaskAnswered:
		return deleteHumanTask(ctx, tx, ev.RunID, ev.NodeID)
	case domain.EffectConfirmationParked:
		return insertHumanTask(ctx, tx, ev.RunID, ev.NodeID, "effect_confirm", env.OccurredAt)
	case domain.EffectConfirmationAnswered:
		return deleteHumanTask(ctx, tx, ev.RunID, ev.NodeID)
	}
	return nil
}

func insertHumanTask(ctx context.Context, tx *sql.Tx, runID, nodeID, kind string, occurredAt time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO human_task_index (run_id, node_id, kind, opened_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (run_id, node_id) DO UPDATE SET kind = excluded.kind, opened_at = excluded.opened_at
	`, runID, nodeID, kind, occurredAt.UTC().Format(time.RFC3339Nano))
	return err
}

func deleteHumanTask(ctx context.Context, tx *sql.Tx, runID, nodeID string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM human_task_index WHERE run_id = ? AND node_id = ?`, runID, nodeID)
	return err
}
