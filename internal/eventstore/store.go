// Package eventstore is the durable event log: the Store interface, the
// single-writer goroutine with group commit, the CAS append, the
// Projection registry, and rebuild/verify. It is the only package (besides
// internal/store/sqlite itself) that imports database/sql — for the
// *sql.Tx/*sql.DB stdlib types the Projection interface needs, never for
// the driver, which stays importable only by internal/store/sqlite
// (12-build-plan.md, "the mechanical decision to get right on day one of
// L02").
package eventstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/events"
	"github.com/williamokano/kairos/internal/store/sqlite"
)

// ErrConflict is returned by AppendIf when expectedSeq does not match the
// stream's actual current sequence — the CAS lost.
var ErrConflict = errors.New("eventstore: append conflict")

// SystemStream is the stream_id L05's engine appends non-run-scoped facts
// to (EngineStarted, EngineStopped, EngineReconciled, ProcessOrphanReaped)
// — no new SQL table, reusing the events table. Both built-in projections
// skip it: it has no RunState to fold into (domain.Advance has no case
// for these event types, by design — see internal/domain/event.go).
const SystemStream = "system"

// AppendMeta is audit metadata shared by every event in one AppendIf call.
// It is per-batch, not per-event: the caller (from L05 on) is a single
// engine decision producing a causally-related set of facts, and no
// consumer needs finer granularity yet — widening this is a deferred,
// documented gap, not a silent one.
type AppendMeta struct {
	Actor         string
	CorrelationID string
	CausationSeq  *int64
	OccurredAt    time.Time
}

// Store is the durable event log's public surface.
type Store interface {
	// AppendIf appends evs to streamID if its current sequence equals
	// expectedSeq (optimistic concurrency), applying every registered
	// Projection in the same transaction. It returns ErrConflict on a
	// losing CAS.
	AppendIf(ctx context.Context, streamID string, expectedSeq int, evs []domain.Event, meta AppendMeta) ([]events.Envelope, error)
	// Read returns every envelope in streamID, in sequence order.
	Read(ctx context.Context, streamID string) ([]events.Envelope, error)
	// ReadAll returns up to limit envelopes with global_seq > afterGlobalSeq.
	ReadAll(ctx context.Context, afterGlobalSeq int64, limit int) ([]events.Envelope, error)
	// Subscribe returns a channel of envelopes published after commit, and
	// an unsubscribe func.
	Subscribe(ctx context.Context) (<-chan events.Envelope, func())
	// Verify replays every stream and diffs it against the persisted
	// projections, reporting any mismatch.
	Verify(ctx context.Context) (VerifyReport, error)
	// Rebuild forces every registered projection to Reset and replay,
	// regardless of its recorded version.
	Rebuild(ctx context.Context) error
	// ListRuns reads the run_index projection, optionally filtered by
	// status. Backs `kairos ls`.
	ListRuns(ctx context.Context, status *domain.RunStatus) ([]RunSummary, error)
	// GetRunState reads the run_state_projection blob for runID, folded
	// via domain.Advance at write time (see projection_runstate.go) — not
	// re-derived here. Backs `kairos show`.
	GetRunState(ctx context.Context, runID string) (domain.RunState, bool, error)
	Close() error
}

// Config configures Open.
type Config struct {
	Path        string
	BackupDir   string
	Registry    *events.Registry
	Projections []Projection // applied in this order; see doc.go for the runstate-before-runindex ordering dependency
}

type store struct {
	writerDB *sql.DB
	readerDB *sql.DB
	registry *events.Registry
	projs    []Projection

	reqs       chan *appendReq
	writerDone chan struct{}
	cancel     context.CancelFunc

	bus *bus
}

// Open opens (creating if absent) the SQLite-backed event store at
// cfg.Path, migrates it, starts the single-writer goroutine, and runs
// verifyProjections to rebuild any projection whose Version() has advanced.
func Open(ctx context.Context, cfg Config) (Store, error) {
	if cfg.Registry == nil {
		return nil, fmt.Errorf("eventstore: Config.Registry is required")
	}

	writerDB, err := sqlite.Open(cfg.Path, sqlite.ModeWriter)
	if err != nil {
		return nil, fmt.Errorf("opening writer connection: %w", err)
	}
	if err := sqlite.Migrate(ctx, writerDB, cfg.BackupDir); err != nil {
		_ = writerDB.Close()
		return nil, fmt.Errorf("migrating: %w", err)
	}

	readerDB, err := sqlite.Open(cfg.Path, sqlite.ModeReader)
	if err != nil {
		_ = writerDB.Close()
		return nil, fmt.Errorf("opening reader connection: %w", err)
	}

	s := &store{
		writerDB:   writerDB,
		readerDB:   readerDB,
		registry:   cfg.Registry,
		projs:      cfg.Projections,
		reqs:       make(chan *appendReq, 256),
		writerDone: make(chan struct{}),
		bus:        newBus(),
	}

	if err := s.verifyProjections(ctx); err != nil {
		_ = writerDB.Close()
		_ = readerDB.Close()
		return nil, fmt.Errorf("verifying projections: %w", err)
	}

	writerCtx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go s.writeLoop(writerCtx)

	return s, nil
}

func (s *store) Close() error {
	s.cancel()
	close(s.reqs)
	<-s.writerDone
	err1 := s.writerDB.Close()
	err2 := s.readerDB.Close()
	if err1 != nil {
		return err1
	}
	return err2
}
