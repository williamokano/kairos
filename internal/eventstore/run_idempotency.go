package eventstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// DedupeRunCreation and RecordRunCreation implement NL-49's fix (see
// migrations/0005_run_idempotency.sql) — mirrors DedupeTrigger/
// RecordTriggerRun's exact two-step claim shape in triggers.go.

func (s *store) DedupeRunCreation(ctx context.Context, key string) (string, bool, error) {
	var runID sql.NullString
	err := s.writerDB.QueryRowContext(ctx, `
		INSERT INTO run_idempotency (idempotency_key, run_id, created_at)
		VALUES (?, NULL, ?)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING run_id
	`, key, time.Now().UTC().Format(time.RFC3339)).Scan(&runID)
	if err == sql.ErrNoRows {
		var existing sql.NullString
		if err := s.readerDB.QueryRowContext(ctx, `SELECT run_id FROM run_idempotency WHERE idempotency_key = ?`, key).Scan(&existing); err != nil {
			return "", false, fmt.Errorf("reading existing run_idempotency row for %s: %w", key, err)
		}
		return existing.String, false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("inserting run_idempotency for %s: %w", key, err)
	}
	return "", true, nil
}

func (s *store) RecordRunCreation(ctx context.Context, key, runID string) error {
	res, err := s.writerDB.ExecContext(ctx, `UPDATE run_idempotency SET run_id = ? WHERE idempotency_key = ?`, runID, key)
	if err != nil {
		return fmt.Errorf("recording run for idempotency key %s: %w", key, err)
	}
	return mustAffectOne(res, "run_idempotency", key)
}
