package eventstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

func (s *store) UpsertSource(ctx context.Context, src Source) error {
	_, err := s.writerDB.ExecContext(ctx, `
		INSERT INTO source (id, kind, config, flow, project, interval_s, enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			kind = excluded.kind, config = excluded.config, flow = excluded.flow,
			project = excluded.project, interval_s = excluded.interval_s, enabled = excluded.enabled
	`, src.ID, src.Kind, src.Config, nullIfEmpty(src.Flow), nullIfEmpty(src.Project), src.IntervalSeconds, boolToInt(src.Enabled))
	if err != nil {
		return fmt.Errorf("upserting source %s: %w", src.ID, err)
	}
	return nil
}

func (s *store) ListSources(ctx context.Context) ([]Source, error) {
	rows, err := s.readerDB.QueryContext(ctx, `
		SELECT id, kind, config, flow, project, interval_s, enabled,
		       health, health_reason, consecutive_errors, last_poll_at, next_poll_at
		FROM source ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("querying source: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Source
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

func (s *store) GetSource(ctx context.Context, id string) (Source, bool, error) {
	row := s.readerDB.QueryRowContext(ctx, `
		SELECT id, kind, config, flow, project, interval_s, enabled,
		       health, health_reason, consecutive_errors, last_poll_at, next_poll_at
		FROM source WHERE id = ?`, id)
	src, err := scanSource(row)
	if err == sql.ErrNoRows {
		return Source{}, false, nil
	}
	if err != nil {
		return Source{}, false, fmt.Errorf("querying source %s: %w", id, err)
	}
	return src, true, nil
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanSource(r rowScanner) (Source, error) {
	var (
		src                         Source
		flow, project, healthReason sql.NullString
		lastPollAt, nextPollAt      sql.NullString
		enabled                     int
	)
	if err := r.Scan(&src.ID, &src.Kind, &src.Config, &flow, &project, &src.IntervalSeconds, &enabled,
		&src.Health.Status, &healthReason, &src.Health.ConsecutiveErrors, &lastPollAt, &nextPollAt); err != nil {
		return Source{}, err
	}
	src.Flow = flow.String
	src.Project = project.String
	src.Enabled = enabled != 0
	src.Health.Reason = healthReason.String
	if lastPollAt.Valid {
		t, err := time.Parse(time.RFC3339, lastPollAt.String)
		if err == nil {
			src.Health.LastPollAt = &t
		}
	}
	if nextPollAt.Valid {
		t, err := time.Parse(time.RFC3339, nextPollAt.String)
		if err == nil {
			src.Health.NextPollAt = &t
		}
	}
	return src, nil
}

func (s *store) SetSourceEnabled(ctx context.Context, id string, enabled bool) error {
	res, err := s.writerDB.ExecContext(ctx, `UPDATE source SET enabled = ? WHERE id = ?`, boolToInt(enabled), id)
	if err != nil {
		return fmt.Errorf("updating source %s enabled: %w", id, err)
	}
	return mustAffectOne(res, "source", id)
}

func (s *store) SetSourceHealth(ctx context.Context, id string, h SourceHealth) error {
	res, err := s.writerDB.ExecContext(ctx, `
		UPDATE source SET health = ?, health_reason = ?, consecutive_errors = ?, last_poll_at = ?, next_poll_at = ?
		WHERE id = ?`,
		h.Status, nullIfEmpty(h.Reason), h.ConsecutiveErrors, timeOrNil(h.LastPollAt), timeOrNil(h.NextPollAt), id)
	if err != nil {
		return fmt.Errorf("updating source %s health: %w", id, err)
	}
	return mustAffectOne(res, "source", id)
}

func (s *store) GetSourceCursor(ctx context.Context, sourceID string) (string, string, bool, error) {
	var cursor, etag sql.NullString
	err := s.readerDB.QueryRowContext(ctx, `SELECT cursor, etag FROM source_cursor WHERE source_id = ?`, sourceID).
		Scan(&cursor, &etag)
	if err == sql.ErrNoRows {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("querying source_cursor for %s: %w", sourceID, err)
	}
	return cursor.String, etag.String, true, nil
}

func (s *store) SetSourceCursor(ctx context.Context, sourceID, cursor, etag string) error {
	_, err := s.writerDB.ExecContext(ctx, `
		INSERT INTO source_cursor (source_id, cursor, etag, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (source_id) DO UPDATE SET cursor = excluded.cursor, etag = excluded.etag, updated_at = excluded.updated_at
	`, sourceID, nullIfEmpty(cursor), nullIfEmpty(etag), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("upserting source_cursor for %s: %w", sourceID, err)
	}
	return nil
}

// DedupeTrigger implements 08-triggers.md's dedupe statement exactly:
// `ON CONFLICT DO NOTHING RETURNING run_id`. Zero rows back from the
// RETURNING clause means the key already existed — the deliberate signal
// this method turns into isNew=false rather than an error, since
// "already triggered" is the expected, common outcome, not a failure.
func (s *store) DedupeTrigger(ctx context.Context, dedupeKey, sourceID, itemID string, expiresAt time.Time) (string, bool, error) {
	var runID sql.NullString
	err := s.writerDB.QueryRowContext(ctx, `
		INSERT INTO trigger_dedupe (dedupe_key, source_id, item_id, run_id, created_at, expires_at)
		VALUES (?, ?, ?, NULL, ?, ?)
		ON CONFLICT (dedupe_key) DO NOTHING
		RETURNING run_id
	`, dedupeKey, nullIfEmpty(sourceID), nullIfEmpty(itemID), time.Now().UTC().Format(time.RFC3339), expiresAt.UTC().Format(time.RFC3339)).Scan(&runID)
	if err == sql.ErrNoRows {
		// The INSERT hit the ON CONFLICT DO NOTHING branch: the key
		// already exists. Read back its recorded run_id (may still be
		// empty if a concurrent creator hasn't called RecordTriggerRun
		// yet — the caller is expected to poll/retry in that rare race).
		var existing sql.NullString
		if err := s.readerDB.QueryRowContext(ctx, `SELECT run_id FROM trigger_dedupe WHERE dedupe_key = ?`, dedupeKey).Scan(&existing); err != nil {
			return "", false, fmt.Errorf("reading existing dedupe row for %s: %w", dedupeKey, err)
		}
		return existing.String, false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("inserting trigger_dedupe for %s: %w", dedupeKey, err)
	}
	return "", true, nil
}

func (s *store) RecordTriggerRun(ctx context.Context, dedupeKey, runID string) error {
	res, err := s.writerDB.ExecContext(ctx, `UPDATE trigger_dedupe SET run_id = ? WHERE dedupe_key = ?`, runID, dedupeKey)
	if err != nil {
		return fmt.Errorf("recording run for dedupe key %s: %w", dedupeKey, err)
	}
	return mustAffectOne(res, "trigger_dedupe", dedupeKey)
}

func mustAffectOne(res sql.Result, table, id string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected on %s: %w", table, err)
	}
	if n == 0 {
		return fmt.Errorf("%s: no such row %q", table, id)
	}
	return nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func timeOrNil(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
