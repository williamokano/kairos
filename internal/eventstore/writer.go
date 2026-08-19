package eventstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/events"
)

// appendReq is one caller's AppendIf call, queued for the writer goroutine.
type appendReq struct {
	ctx         context.Context
	streamID    string
	expectedSeq int
	evs         []domain.Event
	meta        AppendMeta
	reply       chan appendResult
}

type appendResult struct {
	envelopes []events.Envelope
	err       error
}

// AppendIf is the only method on Store that touches s.reqs; it never
// touches s.writerDB directly (TestArchitecture_singleWriter's structural
// half enforces exactly this).
func (s *store) AppendIf(ctx context.Context, streamID string, expectedSeq int, evs []domain.Event, meta AppendMeta) ([]events.Envelope, error) {
	req := &appendReq{
		ctx:         ctx,
		streamID:    streamID,
		expectedSeq: expectedSeq,
		evs:         evs,
		meta:        meta,
		reply:       make(chan appendResult, 1),
	}
	select {
	case s.reqs <- req:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case res := <-req.reply:
		return res.envelopes, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// writeLoop is the single writer goroutine: it is the only code path that
// ever calls .Exec/.BeginTx on s.writerDB. It drains up to 64 pending
// requests or waits 2ms, whichever comes first, then commits them in one
// transaction — one fsync amortised across a burst (06-durability.md).
func (s *store) writeLoop(ctx context.Context) {
	defer close(s.writerDone)
	for {
		var batch []*appendReq
		select {
		case <-ctx.Done():
			return
		case req, ok := <-s.reqs:
			if !ok {
				return
			}
			batch = append(batch, req)
		}

		timeout := time.NewTimer(2 * time.Millisecond)
	drain:
		for len(batch) < 64 {
			select {
			case req, ok := <-s.reqs:
				if !ok {
					break drain
				}
				batch = append(batch, req)
			case <-timeout.C:
				break drain
			}
		}
		timeout.Stop()

		s.commitBatch(ctx, batch)
	}
}

// commitBatch runs every request in batch inside one transaction, each
// wrapped in its own SAVEPOINT so one request's CAS conflict rolls back
// only that request's effect, never a sibling's successful append in the
// same batch.
func (s *store) commitBatch(ctx context.Context, batch []*appendReq) {
	tx, err := s.writerDB.BeginTx(ctx, nil)
	if err != nil {
		for _, req := range batch {
			req.reply <- appendResult{err: fmt.Errorf("beginning batch transaction: %w", err)}
		}
		return
	}

	type pending struct {
		req  *appendReq
		envs []events.Envelope
	}
	var ok []pending

	for i, req := range batch {
		sp := fmt.Sprintf("req_%d", i)
		if _, err := tx.ExecContext(ctx, "SAVEPOINT "+sp); err != nil {
			req.reply <- appendResult{err: fmt.Errorf("savepoint: %w", err)}
			continue
		}
		envs, err := s.appendOne(ctx, tx, req)
		if err != nil {
			_, _ = tx.ExecContext(ctx, "ROLLBACK TO "+sp)
			_, _ = tx.ExecContext(ctx, "RELEASE "+sp)
			req.reply <- appendResult{err: err}
			continue
		}
		if _, err := tx.ExecContext(ctx, "RELEASE "+sp); err != nil {
			req.reply <- appendResult{err: fmt.Errorf("release savepoint: %w", err)}
			continue
		}
		ok = append(ok, pending{req: req, envs: envs})
	}

	if err := tx.Commit(); err != nil {
		for _, p := range ok {
			p.req.reply <- appendResult{err: fmt.Errorf("committing: %w", err)}
		}
		return
	}

	for _, p := range ok {
		p.req.reply <- appendResult{envelopes: p.envs}
		for _, env := range p.envs {
			s.bus.publish(env)
		}
	}
}

// appendOne performs the CAS insert for every event in req, in order,
// applying every registered projection to each successfully-inserted
// event before moving to the next.
func (s *store) appendOne(ctx context.Context, tx *sql.Tx, req *appendReq) ([]events.Envelope, error) {
	envs := make([]events.Envelope, 0, len(req.evs))
	seq := req.expectedSeq
	occurredAt := req.meta.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	recordedAt := time.Now().UTC()

	for _, ev := range req.evs {
		eventType := ev.EventType()
		version, ok := s.registry.CurrentVersion(eventType)
		if !ok {
			return nil, fmt.Errorf("eventstore: no schema registered for event type %q", eventType)
		}
		payload, err := json.Marshal(ev)
		if err != nil {
			return nil, fmt.Errorf("marshalling %s: %w", eventType, err)
		}
		if len(payload) > 65536 {
			return nil, fmt.Errorf("eventstore: %s payload exceeds 64 KiB; push large content to the artifact store", eventType)
		}

		seq++
		res, err := tx.ExecContext(ctx, `
			INSERT INTO events (stream_id, sequence, event_type, event_version, occurred_at, recorded_at, actor, causation_seq, correlation_id, payload)
			SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
			WHERE (SELECT COALESCE(MAX(sequence), 0) FROM events WHERE stream_id = ?) = ?
		`,
			req.streamID, seq, eventType, version,
			occurredAt.Format(time.RFC3339Nano), recordedAt.Format(time.RFC3339Nano),
			req.meta.Actor, req.meta.CausationSeq, req.meta.CorrelationID, string(payload),
			req.streamID, seq-1,
		)
		if err != nil {
			return nil, fmt.Errorf("inserting %s: %w", eventType, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("checking rows affected: %w", err)
		}
		if affected == 0 {
			return nil, ErrConflict
		}
		globalSeq, err := res.LastInsertId()
		if err != nil {
			return nil, fmt.Errorf("reading global_seq: %w", err)
		}

		env := events.Envelope{
			StreamID:      req.streamID,
			Sequence:      seq,
			GlobalSeq:     globalSeq,
			EventType:     eventType,
			EventVersion:  version,
			OccurredAt:    occurredAt,
			RecordedAt:    recordedAt,
			Actor:         req.meta.Actor,
			CausationSeq:  req.meta.CausationSeq,
			CorrelationID: req.meta.CorrelationID,
			Event:         ev,
		}

		for _, p := range s.projs {
			if err := p.Apply(ctx, tx, env); err != nil {
				return nil, fmt.Errorf("projection %s applying %s: %w", p.Name(), eventType, err)
			}
		}

		envs = append(envs, env)
	}
	return envs, nil
}
