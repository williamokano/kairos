package eventstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/williamokano/kairos/internal/events"
)

// scanEnvelope reads one events row into an Envelope, decoding its payload
// via the registry into the concrete domain.Event value.
func (s *store) scanEnvelope(rows interface {
	Scan(dest ...any) error
}) (events.Envelope, error) {
	var (
		globalSeq                           int64
		streamID, eventType                 string
		sequence, version                   int
		occurredAt, recordedAt, actor, corr string
		causationSeq                        sql.NullInt64
		payload                             string
	)
	if err := rows.Scan(&globalSeq, &streamID, &sequence, &eventType, &version, &occurredAt, &recordedAt, &actor, &causationSeq, &corr, &payload); err != nil {
		return events.Envelope{}, fmt.Errorf("scanning event row: %w", err)
	}

	ev, err := s.registry.Decode(eventType, version, []byte(payload))
	if err != nil {
		return events.Envelope{}, fmt.Errorf("decoding %s v%d at global_seq %d: %w", eventType, version, globalSeq, err)
	}

	occurred, err := time.Parse(time.RFC3339Nano, occurredAt)
	if err != nil {
		return events.Envelope{}, fmt.Errorf("parsing occurred_at: %w", err)
	}
	recorded, err := time.Parse(time.RFC3339Nano, recordedAt)
	if err != nil {
		return events.Envelope{}, fmt.Errorf("parsing recorded_at: %w", err)
	}

	env := events.Envelope{
		StreamID:      streamID,
		Sequence:      sequence,
		GlobalSeq:     globalSeq,
		EventType:     eventType,
		EventVersion:  version,
		OccurredAt:    occurred,
		RecordedAt:    recorded,
		Actor:         actor,
		CorrelationID: corr,
		Event:         ev,
	}
	if causationSeq.Valid {
		env.CausationSeq = &causationSeq.Int64
	}
	return env, nil
}

const eventColumns = `global_seq, stream_id, sequence, event_type, event_version, occurred_at, recorded_at, actor, causation_seq, correlation_id, payload`

func (s *store) Read(ctx context.Context, streamID string) ([]events.Envelope, error) {
	rows, err := s.readerDB.QueryContext(ctx, `SELECT `+eventColumns+` FROM events WHERE stream_id = ? ORDER BY sequence ASC`, streamID)
	if err != nil {
		return nil, fmt.Errorf("querying stream %s: %w", streamID, err)
	}
	defer func() { _ = rows.Close() }()
	return s.scanAll(rows)
}

func (s *store) ReadAll(ctx context.Context, afterGlobalSeq int64, limit int) ([]events.Envelope, error) {
	rows, err := s.readerDB.QueryContext(ctx, `SELECT `+eventColumns+` FROM events WHERE global_seq > ? ORDER BY global_seq ASC LIMIT ?`, afterGlobalSeq, limit)
	if err != nil {
		return nil, fmt.Errorf("querying all after %d: %w", afterGlobalSeq, err)
	}
	defer func() { _ = rows.Close() }()
	return s.scanAll(rows)
}

func (s *store) scanAll(rows *sql.Rows) ([]events.Envelope, error) {
	var out []events.Envelope
	for rows.Next() {
		env, err := s.scanEnvelope(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, env)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating rows: %w", err)
	}
	return out, nil
}
