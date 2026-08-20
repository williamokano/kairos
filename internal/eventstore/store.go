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
	"strings"
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

// ConversationStreamPrefix namespaces a Conversation's own stream —
// stream_id = ConversationStreamPrefix+runID (L14 scopes Conversation 1:1
// with Run — see L14-conversations.md's Documented decisions). Same
// posture as SystemStream: reuses the events table, no RunState to fold
// into, skipped by both built-in projections and Verify/Rebuild.
const ConversationStreamPrefix = "conversation:"

// ConversationStreamID returns the stream_id for runID's Conversation.
func ConversationStreamID(runID string) string { return ConversationStreamPrefix + runID }

// RunIDFromConversationStream extracts the runID from a stream_id built by
// ConversationStreamID, reporting false if streamID is not a conversation
// stream.
func RunIDFromConversationStream(streamID string) (string, bool) {
	if !strings.HasPrefix(streamID, ConversationStreamPrefix) {
		return "", false
	}
	return strings.TrimPrefix(streamID, ConversationStreamPrefix), true
}

// IsAuxStream reports whether streamID is one of the non-run-scoped
// streams (system, or any Conversation) that domain.Advance has no case
// for and that both built-in projections and Verify/Rebuild must skip
// rather than attempt to fold — the single check point every such site
// consults, so a third aux-stream namespace is a one-line addition here
// instead of four.
func IsAuxStream(streamID string) bool {
	return streamID == SystemStream || strings.HasPrefix(streamID, ConversationStreamPrefix)
}

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

	// UpsertSource creates or updates a trigger source's config row
	// (08-triggers.md: "state, all owned by the daemon, never the
	// plugin"). Health fields are untouched — see SetSourceHealth.
	UpsertSource(ctx context.Context, src Source) error
	// ListSources reads every registered source, ordered by id.
	ListSources(ctx context.Context) ([]Source, error)
	// GetSource reads one source by id.
	GetSource(ctx context.Context, id string) (Source, bool, error)
	// SetSourceEnabled flips a source's enabled flag — `kairos src
	// pause`/`resume`.
	SetSourceEnabled(ctx context.Context, id string, enabled bool) error
	// SetSourceHealth records a poll cycle's outcome for `kairos status`.
	SetSourceHealth(ctx context.Context, id string, health SourceHealth) error
	// GetSourceCursor reads a source's persisted cursor/etag; ok is false
	// for a source that has never completed a poll.
	GetSourceCursor(ctx context.Context, sourceID string) (cursor, etag string, ok bool, err error)
	// SetSourceCursor persists a source's cursor/etag after a poll —
	// "cursor state stays owned by the daemon, never the plugin" (ADR
	// 0011).
	SetSourceCursor(ctx context.Context, sourceID, cursor, etag string) error
	// DedupeTrigger is 08-triggers.md's one INSERT that is the difference
	// between triggers working and not: `INSERT ... ON CONFLICT
	// (dedupe_key) DO NOTHING RETURNING run_id`. isNew is true and
	// existingRunID is "" the first time dedupeKey is seen (the caller
	// must then create the run and call RecordTriggerRun); isNew is false
	// and existingRunID names the run already created for every
	// subsequent call with the same key. expiresAt is stamped by the
	// caller (08-triggers.md: "+30d") so this package stays clock-free.
	DedupeTrigger(ctx context.Context, dedupeKey, sourceID, itemID string, expiresAt time.Time) (existingRunID string, isNew bool, err error)
	// RecordTriggerRun fills in the run_id column DedupeTrigger left
	// empty, once the caller has actually created the run — two steps,
	// not one, because DedupeTrigger must claim the key BEFORE the
	// (possibly slow) run-creation call, or two concurrent pollers both
	// pass an empty-run_id race.
	RecordTriggerRun(ctx context.Context, dedupeKey, runID string) error
	Close() error
}

// Source is one configured trigger source's persisted row.
type Source struct {
	ID, Kind, Config, Flow, Project string
	IntervalSeconds                 int
	Enabled                         bool
	Health                          SourceHealth
}

// SourceHealth is a poll cycle's recorded outcome — healthy | throttled |
// unhealthy, per 08-triggers.md's `source.health` column.
type SourceHealth struct {
	Status            string // "unknown" | "healthy" | "throttled" | "unhealthy"
	Reason            string
	ConsecutiveErrors int
	LastPollAt        *time.Time
	NextPollAt        *time.Time
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
