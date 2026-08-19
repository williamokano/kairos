# L02 — Event Store (SQLite)

## Depends on

L00 (bootstrap) and L01 (domain model). Per the mermaid build order, L02 depends on both — the
domain must exist with no store to depend on (L01 before L02), and the event store's schema
registry decodes exactly the sixteen `domain.Event` types L01 defined.

## Scope

**In.**
- **`internal/store/sqlite`**: `Open(path, mode)` (writer: `MaxOpenConns(1)`, `_txlock=immediate`;
  reader: `MaxOpenConns(8)`) with every PRAGMA `06-durability.md` specifies verbatim (WAL,
  `synchronous=FULL`, `fullfsync`, per-connection `foreign_keys=ON`, `busy_timeout=5000`,
  `temp_store=MEMORY`, `cache_size=-65536`); `Migrate(ctx, db, backupDir)` — a forward-only
  runner over `embed.FS`, `VACUUM INTO` backup before applying to a database with prior schema;
  migration 0001 (the `events` table DDL, verbatim) and 0002 (`projection_offsets`,
  `run_state_projection`, `run_index`).
- **`internal/events`**: `Envelope` (the audit-field wrapper — see Documented decisions #1); a
  JSON-Schema registry (`santhosh-tekuri/jsonschema/v6`, draft 2020-12) with `Register`/
  `Validate`/`New`/`NewVersion`/`CurrentVersion`/`Decode`; `Builtin()` registering all sixteen
  `domain.Event` types at schema version 1, embedded via `embed.FS`; a fixture corpus (one
  `fixtures/<type>/v1.json` per type) and `TestEvents_allHistoricalFixturesProject`; a `redact.go`
  stub (real logic is L11's).
- **`internal/eventstore`**: `Store` interface (`AppendIf`/`Read`/`ReadAll`/`Subscribe`/`Verify`/
  `Rebuild`/`Close`), `AppendMeta`; the single-writer goroutine with group commit (drain up to 64
  requests or 2ms, whichever first; one `COMMIT`/fsync per batch; per-request `SAVEPOINT`s so one
  conflicting request doesn't poison its batch-mates); the CAS insert exactly as
  `06-durability.md` specifies; a post-commit in-process bus; the `Projection` interface and two
  implementations (`RunStateProjection`, `RunIndexProjection`); boot-time `verifyProjections`
  (version-mismatch triggers `Reset`+replay) plus library-level `Verify()`/`Rebuild()`.
- **`internal/archtest`**: `TestArchitecture_noSQLOutsideStore` widened to forbid
  `modernc.org/sqlite` (no exemption, unlike `database/sql`, which is now exempted for
  `internal/eventstore` too — see decision #4); `TestArchitecture_singleWriter` implemented for
  real (was `t.Skip`'d since L00).
- `BenchmarkAppendIf_singleEvent`, gated at p99 < 5ms against a real temp-file database.
- `go.mod`: `modernc.org/sqlite`, `github.com/santhosh-tekuri/jsonschema/v6`.

**Out.**
- Any CLI verb (`kairos db verify`/`reindex`) — L04. See decision #5.
- The engine's advance loop and dispatch to services — L05. `Store` has no consumer yet; proof is
  tests, matching L00/L01's own pattern.
- Effects, policy, redaction's real logic — L11/L12.
- Fork/replay-for-fork mechanics (`run.forked`, prefix copy) — L18. `Verify()` here is a
  from-scratch consistency check, not fork's replay-and-diff machinery.
- Artifact store — the 64 KiB payload cap is enforced (`appendOne` rejects larger payloads), but
  there is no artifact store to push overflow content into yet.

## Documented decisions

1. **Audit fields live on `events.Envelope`, never on `domain.Event`.** `domain.Event` stays pure
   (L01/AGENTS §4): no `Actor`, `CausationSeq`, `CorrelationID`, or timestamps on the domain
   types themselves. The store assigns `Sequence`/`GlobalSeq`/`RecordedAt`; the caller supplies
   `Actor`/`CorrelationID`/`CausationSeq`/`OccurredAt` via `AppendMeta`.
2. **`AppendMeta` is per-batch, not per-event.** One `AppendIf` call's events share one
   `Actor`/`CorrelationID`/`CausationSeq`. No consumer needs finer granularity before L05's
   `RunAdvanced{cmds}` pattern exists; widening to per-event is a deferred, documented gap.
3. **`Projection.Apply` takes `events.Envelope`, not a bare `domain.Event`.**
   `06-durability.md`'s signature (`Apply(ctx, tx, ev Event)`) predates the concrete split
   between `domain.Event` (pure) and `events.Envelope` (audit-wrapped) this corpus later
   introduced. A projection generally needs `StreamID` (the run ID) and `OccurredAt` (a
   deterministic "now" for folding through `domain.Advance`), neither of which `domain.Event`
   carries — so `Envelope` is the correct parameter.
4. **`internal/eventstore` is exempted from `noSQLOutsideStore`'s `database/sql` check** (types
   only — `*sql.Tx` in the `Projection` interface, sanctioned by `12-build-plan.md`'s "day one of
   L02" note); `modernc.org/sqlite` itself stays a true one-file-only import with no exemption,
   enforced as a second, separate forbidden-import check.
5. **`kairos db verify`/`reindex` CLI verbs are deferred to L04.** L02 ships `Verify()`/
   `Rebuild()` as library functions, proven by `TestStore_rebuildIsByteIdentical` and
   `TestStore_appendIfAppliesProjectionsInTheSameTransaction`, not a CLI surface —
   `internal/cli` doesn't exist until L04 (L00-bootstrap.md's own Out section).
6. **Two projections, both fold via `domain.Advance`; `RunStateProjection` must be registered
   before `RunIndexProjection`.** `RunIndexProjection.Apply` reads the `status` column
   `RunStateProjection` already wrote earlier in the same transaction, rather than re-folding
   independently — cheaper and avoids two divergent sources of truth about a run's status. This
   ordering dependency is documented on both types and enforced by `Config.Projections` order,
   not by a runtime check (L02's scale doesn't warrant one; a future document may add it if
   registration order stops being visibly correct at a glance).
7. **`TestArchitecture_singleWriter` checks that `Store.AppendIf`'s method body never references
   the writer `*sql.DB` field directly** — the public write entrypoint must only ever send to the
   writer goroutine's request channel. This is paired with a runtime proof
   (`TestStore_concurrentAppendsNeverSurfaceSQLITEBUSY`, in `internal/eventstore`, not
   `internal/archtest`): 50 concurrent `AppendIf` calls against distinct streams, asserting no
   `SQLITE_BUSY` ever surfaces. The architecture test alone cannot prove the runtime guarantee
   (that's a connection-concurrency property, not an import-graph property); together they cover
   both the structural contract and its behavioral consequence.
8. **`verifyProjections` (boot-time) and `Rebuild()` use `s.writerDB` directly, not the request
   queue** — both run either before the writer goroutine starts (`verifyProjections`, called from
   `Open` before `go s.writeLoop(...)`) or as an explicit administrative operation
   (`Rebuild`). This is a narrower guarantee than "literally every write funnels through the
   goroutine": it is "every write reachable by another package funnels through the goroutine,"
   which is what `noSQLOutsideStore` plus decision #7's check together enforce. Registered here
   rather than glossed over.
9. **Rebuild reads are fully buffered into memory before replay.** `rebuildOne` and `Verify`
   collect all matching event rows via `scanAll` and close the cursor *before* calling
   `Projection.Apply`/`domain.Advance` in a loop, because `Apply`'s own queries (e.g.
   `RunStateProjection` reading the current blob) would otherwise run nested statements against
   the same SQLite connection while an outer `*sql.Rows` cursor is still open — not reliably
   supported. Acceptable at L02's scale; a future document may need streaming replay for very
   large logs.

## Public interfaces

```go
// internal/store/sqlite
func Open(path string, mode ConnMode) (*sql.DB, error)
func Migrate(ctx context.Context, db *sql.DB, backupDir string) error

// internal/events
type Envelope struct { StreamID string; Sequence int; GlobalSeq int64; EventType string
    EventVersion int; OccurredAt, RecordedAt time.Time; Actor string
    CausationSeq *int64; CorrelationID string; Event domain.Event }
func NewRegistry() *Registry
func Builtin() (*Registry, error)
func (r *Registry) Register(eventType string, version int, schemaJSON []byte, zero ZeroFunc) error
func (r *Registry) Decode(eventType string, version int, payload []byte) (domain.Event, error)
func (r *Registry) CurrentVersion(eventType string) (int, bool)

// internal/eventstore
type AppendMeta struct { Actor, CorrelationID string; CausationSeq *int64; OccurredAt time.Time }
type Store interface {
    AppendIf(ctx context.Context, streamID string, expectedSeq int, evs []domain.Event, meta AppendMeta) ([]events.Envelope, error)
    Read(ctx context.Context, streamID string) ([]events.Envelope, error)
    ReadAll(ctx context.Context, afterGlobalSeq int64, limit int) ([]events.Envelope, error)
    Subscribe(ctx context.Context) (<-chan events.Envelope, func())
    Verify(ctx context.Context) (VerifyReport, error)
    Rebuild(ctx context.Context) error
    Close() error
}
func Open(ctx context.Context, cfg Config) (Store, error)

type Projection interface {
    Name() string
    Version() int
    Apply(ctx context.Context, tx *sql.Tx, env events.Envelope) error
    Reset(ctx context.Context, tx *sql.Tx) error
}
type RunStateProjection struct{} // folds via domain.Advance, upserts a RunState blob
type RunIndexProjection struct{} // narrow run_id/status/started_at/updated_at index
```

## Files to create

```
internal/store/sqlite/open.go
internal/store/sqlite/migrate.go
internal/store/sqlite/migrations/0001_events.sql
internal/store/sqlite/migrations/0002_projections.sql
internal/store/sqlite/open_test.go
internal/store/sqlite/migrate_test.go

internal/events/envelope.go
internal/events/registry.go
internal/events/init.go
internal/events/redact.go
internal/events/schemas/<event_type>/v1.json    # x16
internal/events/fixtures/<event_type>/v1.json   # x16
internal/events/registry_test.go
internal/events/fixtures_test.go

internal/eventstore/store.go
internal/eventstore/writer.go
internal/eventstore/bus.go
internal/eventstore/projection.go
internal/eventstore/projection_runstate.go
internal/eventstore/projection_runindex.go
internal/eventstore/rebuild.go
internal/eventstore/replay.go
internal/eventstore/store_test.go
internal/eventstore/writer_concurrency_test.go
internal/eventstore/rebuild_test.go
internal/eventstore/append_bench_test.go

internal/archtest/single_writer_test.go
internal/archtest/fixtures/singlewriter/violation.go
internal/archtest/fixtures/sqlitedriveroutsidestore/violation.go
# modified: internal/archtest/no_sql_outside_store_test.go, deferred_test.go
```

## Data changes

`~/.kairos/kairos.db` now has real schema: `events` (STRICT, `AUTOINCREMENT`, both `CHECK`s,
`UNIQUE(stream_id, sequence)`, two indexes), `schema_migrations`, `projection_offsets`,
`run_state_projection`, `run_index`. This is the first document to create the database file at
all.

## Acceptance criteria

- `go build ./...`, `go vet ./...`, `golangci-lint run` clean; `go test ./... -race` green (49
  tests passing across `internal/domain`, `internal/events`, `internal/eventstore`,
  `internal/store/sqlite`, `internal/archtest`).
- `TestArchitecture_noSQLOutsideStore` passes with the widened table, and both its
  `fixtureIsCaught` and `driverFixtureIsCaught` subtests trip their fixtures.
- `TestArchitecture_singleWriter` is real (no longer skipped); its fixture trips it.
- `TestEvents_allHistoricalFixturesProject` passes for all sixteen event types.
- `TestStore_appendIfFailsOnSequenceConflict` proves the CAS; `TestStore_concurrentAppendsNeverSurfaceSQLITEBUSY`
  proves 50 concurrent `AppendIf` calls never surface `SQLITE_BUSY`.
- `TestStore_rebuildIsByteIdentical` proves `Rebuild()` reproduces identical projection contents.
- `BenchmarkAppendIf_singleEvent`: p99 = 3.3ms locally, under the 5ms gate (`b.Fatalf` makes this
  a hard CI gate).
- `CGO_ENABLED=0` cross-builds for darwin/linux × arm64/amd64 succeed for
  `internal/store/sqlite`, `internal/events`, and `internal/eventstore` directly (verified with
  `go build` against each package, not just `./cmd/kairos`, since nothing imports them yet) —
  this is the real test of `modernc.org/sqlite`'s pure-Go promise, the whole reason it was chosen.
- No `TODO`, `FIXME`, or commented-out code in the diff.

## Tests

- `internal/store/sqlite`: writer/reader connection mode, PRAGMA effectiveness, migration
  idempotency and table creation, backup-before-migrate.
- `internal/events`: registry registration/validation/decode, the fixture-projection walk.
- `internal/eventstore`: CAS success/conflict, read ordering, projection application within the
  append transaction, 50-way concurrent append, rebuild byte-identity, the append benchmark.
- `internal/archtest`: the widened SQL-outside-store check (two forbidden-import lists, two
  fixtures) and the new single-writer check (one fixture).

## Benchmarks

`BenchmarkAppendIf_singleEvent` — single-event append latency end-to-end (CAS insert, two
projection `Apply` calls, commit, fsync), p99 gated at < 5ms, against a real temp-file database
per iteration on a distinct stream. Measured locally: p99 ≈ 3.3ms.

## Migration

None from a prior version — this document creates the schema. Forward migrations from here on
follow the numbered `migrations/000N_*.sql` convention `migrate.go` implements.

## Future work

- L03 (definition + validator) is the first real producer of `RunStarted{Graph}` from parsed
  YAML — nothing in L02 changes shape for it.
- L04 (daemon: API + SSE + CLI) adds the `kairos db verify`/`reindex` CLI verbs calling
  `Store.Verify()`/`Rebuild()`, and is where `internal/cli` is finally extracted.
- L05 (engine) is `Store`'s first real consumer: the advance loop calls `AppendIf` with the
  `Cmd`s `domain.Advance` returns, using `GlobalSeq` from one `AppendIf` call's result to set
  `CausationSeq` on the next.
- L11 (policy + secrets) gives `events.Redact` real logic once effects can carry credentials or
  PII into a payload.
- L17 (child runs) is where `RunDegraded`'s actual trigger condition is computed; L02's
  `RunStateProjection` already folds it correctly via `domain.Advance` since that logic lives in
  L01, not here.
- A future high-volume deployment may need `rebuildOne`/`Verify` to stream rather than fully
  buffer events in memory (decision #9) — not needed at L02's scale.
