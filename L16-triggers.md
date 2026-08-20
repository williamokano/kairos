# L16 — Triggers: the one code path work arrives through

## Depends on

L14 (conversations), transitively L00-L11. Confirmed the correct next document per
`12-build-plan.md`'s numbering-as-sequence rule: L15 needs L18 (not yet built) and L17 needs both L06
and L07 — L16 needs only L14, already satisfied.

## Scope

**In.**
- `internal/tasksource`: the `TaskSource` contract (`Describe`/`Poll`/`Ack`), `WorkItem`
  (`DedupeKey`/`Project`/`Body`/`Budget` per the doc), the closed seven-code error set.
- **The one code path**: `CreateRun`/`TriggerRun` — every way a Run comes into existence (`kairos
  run`, the inbox, a poller, cron, a webhook) now calls the same function. `internal/api`'s `POST
  /runs` handler was refactored to call it too, replacing L04's inline two-`AppendIf` sequence with
  a shared implementation.
- The inbox: `~/.kairos/inbox/*.md`, fsnotify plus a poll fallback, a 2s quiet period, atomic
  `rename` into `.taken/` as the primary dedupe mechanism, content-hash (`sha256`) dedupe as a
  second safety net, `.dup/`/`.failed/` outcomes.
- The generic poller: jittered scheduling, ETag round-tripping, exponential backoff capped at 30m,
  `retryAfter` honoured exactly on `rate_limited`, unhealthy after 5 consecutive errors or 3
  non-advancing-cursor-with-items polls.
- `cron` sources: `Daily`/`Weekly` schedules, `catchUp: skip` (never a backlog), wall-clock-jump
  cold-start detection.
- The `volume:` block as real engine support: `VolumeController` implements debounce, digest
  batching (`maxItems`/`window`), and rate/queue-depth-triggered degrade-to-batch.
- New SQLite tables (`source`, `source_cursor`, `trigger_dedupe`), owned entirely by
  `internal/eventstore` (extending `Store`, not a new SQL-importing package) — never by a plugin.
- The dedupe statement exactly as specified: `INSERT ... ON CONFLICT (dedupe_key) DO NOTHING
  RETURNING run_id`.
- Opt-in webhooks: HMAC-SHA256 verified before parsing, a bad signature returns 404 with no
  distinguishing body, redelivery absorbed by the same dedupe table.
- The stdio NDJSON plugin contract: one-shot invocation through `internal/executor/local` (the sole
  chokepoint), the request/response envelope, the closed error-code set enforced on the way back in,
  secrets as `KAIROS_SECRET_<NAME>` env vars (never in the request body), a `secret.accessed` event
  per call.
- `ack` routed through an idempotency check (see Documented decision #3) — never a direct,
  un-deduplicated call.
- Admission integration: `QueueLimits{MaxQueued, MaxOpenDecisions}` — trigger-created runs are
  rejected, not silently dropped or unboundedly queued, once either threshold is crossed.
- `kairos src add/ls/pause/resume` + `POST/GET /sources`, `POST /sources/{id}/pause|resume`.
- The two named tests: `TestEngine_everyRunHasATraceableTrigger`,
  `TestArchitecture_runCreationNotReachableFromActors`.

**Out.** Real `github`/`jira`/`linear` `TaskSource` providers (NL-41); stream-mode plugin invocation
for webhook-fed sources (NL-42); live pause/resume of an already-running poller (NL-43); full
cron(5)-expression scheduling (NL-44); `repo-watch` (NL-45); an embedded tunnel client or
Kairos-operated relay for webhooks (rejected outright by the doc, not deferred); a Go-plugin or
gRPC-over-socket extension mechanism (also rejected by the doc).

## Documented decisions

1. **`internal/tasksource` owns run-creation, not `internal/api`.** `POST /runs`'s handler now calls
   `tasksource.CreateRun` instead of containing its own `AppendIf` sequence — the two were byte-for-
   byte identical before this document, so consolidating them is the literal meaning of "one code
   path out," not a refactor for its own sake. `internal/engine`'s actor-dispatch code never imports
   `internal/tasksource` (`TestArchitecture_runCreationNotReachableFromActors`), so an actor can
   never synthesise a Run no trigger authorised (`01-architecture.md`'s L15).
2. **CLI-created runs pass `QueueLimits{}` (unchecked).** 08-triggers.md's `maxQueued`/
   `maxOpenDecisions` backpressure targets trigger-created backlog specifically; a human typing
   `kairos run` right now is not "backlog." Every trigger source in this package passes real,
   configured limits.
3. **`ack`'s idempotency reuses `trigger_dedupe`'s primitive (`"ack:" + idempotencyKey`), not
   `internal/effect`'s `Provider` interface.** `effect.Provider` is scoped to a `RunID`/`NodeID`/
   `ExecID` triple (an in-run node execution); an `ack` call happens outside any node execution —
   often before a run even exists (the poll-then-ack-rejection cycle). The idempotency *shape*
   effects use (probe-before-act, replay returns the recorded result without acting) is preserved;
   the machinery is the simpler primitive that already fits.
4. **`TriggerRun` claims the dedupe key before creating the run, in two steps.** `DedupeTrigger`
   inserts with `run_id = NULL` first; `RecordTriggerRun` fills it in after `CreateRun` succeeds.
   Two concurrent callers racing the same key: exactly one wins the INSERT and proceeds to create the
   run; the other observes `isNew=false` with (usually) an already-recorded run id, or — in the
   narrow window between the two steps — a distinct, honestly-surfaced "claimed concurrently, not
   yet recorded" error rather than a silently wrong answer. Proven under `-race` with 10 concurrent
   callers racing one key (`TestTriggerRun_concurrentSameKeyProducesExactlyOneRun`).
5. **No cron(5)-expression parser.** `Schedule` has exactly two implementations, `Daily`/`Weekly`,
   covering 08-triggers.md's own examples precisely. AGENTS.md's approved-dependency table names no
   cron library; adding one is an ADR this narrow-scoped document does not need to spend (NL-44).
6. **A `cron`-kind source reuses `source_cursor.etag` to hold "last fired," not a fourth table.**
   Cron has no upstream cursor; the existing column is repurposed rather than adding
   `source_last_fired` for one timestamp.
7. **Webhook payload parsing is a direct Go callback (`WebhookConfig.Parse`), not a stream-mode
   plugin round trip.** 08-triggers.md frames webhook-fed sources as running in the (opt-in) stream
   mode this document does not implement (NL-42) — the HMAC/dedupe machinery around `Parse` is real
   and identical either way; only the *source* of the parsed `WorkItem` differs.
8. **`Registry.Build`'s only registered kind is `"fake"`.** The extension point (`Register`) is real
   and tested; `github`/`jira`/`linear` constructors are the largest single deferred item (NL-41) —
   registering them later is a one-file change, which is the entire reason the registry exists now
   rather than being added alongside the first real provider.
9. **A trigger-created run's `Actor` is `"trigger:<kind>"`** (`trigger:inbox`, `trigger:poll`,
   `trigger:cron`, `trigger:webhook`), distinct from `"cli"` — `AppendMeta.Actor` already existed
   (L02); this document is its first real consumer beyond `"cli"`/`"engine"`.

## Public interfaces

```go
// internal/tasksource
type WorkItem struct { ID, DedupeKey, Title, Body, Project, Flow string; Params json.RawMessage; Priority int; Budget float64; Labels map[string]string }
type Source interface {
	Describe(ctx context.Context) (Descriptor, error)
	Poll(ctx context.Context, in PollInput) (PollOutput, error)
	Ack(ctx context.Context, in AckInput) (AckOutput, error)
}

type CreateRunRequest struct { DefinitionRef string; Params json.RawMessage; TriggerRef, Actor string; ParentRunID *string }
type QueueLimits struct { MaxQueued, MaxOpenDecisions int }
func CreateRun(ctx context.Context, store eventstore.Store, req CreateRunRequest, limits QueueLimits) (runID string, status domain.RunStatus, err error)
func TriggerRun(ctx context.Context, store eventstore.Store, dedupeKey, sourceID, itemID string, req CreateRunRequest, limits QueueLimits) (runID string, created bool, err error)
func Ack(ctx context.Context, store eventstore.Store, src Source, in AckInput) (AckOutput, error)

type VolumeController struct{ /* ... */ }
func NewVolumeController(cfg VolumeConfig, out chan<- Flush) *VolumeController
func (v *VolumeController) Add(item WorkItem, queueDepth int)

type Schedule interface{ Next(after time.Time) time.Time }
func CronCatchUp(sched Schedule, lastFired, now time.Time) (nextFire time.Time, coldStart bool)

func RunInbox(ctx context.Context, cfg InboxConfig, store eventstore.Store) error
type Poller struct{ /* ... */ }
func NewPoller(cfg PollerConfig, store eventstore.Store) *Poller
func (p *Poller) Run(ctx context.Context)

type Plugin struct{ Name, Path string; Config json.RawMessage; Secrets map[string]string; Exec local.Executor; /* ... */ } // implements Source
func Handler(cfg WebhookConfig, store eventstore.Store) http.HandlerFunc

type Manager struct{ /* ... */ }
func NewManager(cfg ManagerConfig, store eventstore.Store) *Manager
func (m *Manager) Start(ctx context.Context) error
func (m *Manager) Stop()

// internal/eventstore, added to Store
UpsertSource(ctx, Source) error
ListSources(ctx) ([]Source, error)
GetSource(ctx, id string) (Source, bool, error)
SetSourceEnabled(ctx, id string, enabled bool) error
SetSourceHealth(ctx, id string, health SourceHealth) error
GetSourceCursor(ctx, sourceID string) (cursor, etag string, ok bool, err error)
SetSourceCursor(ctx, sourceID, cursor, etag string) error
DedupeTrigger(ctx, dedupeKey, sourceID, itemID string, expiresAt time.Time) (existingRunID string, isNew bool, err error)
RecordTriggerRun(ctx, dedupeKey, runID string) error
```

## Files to create

```
internal/tasksource/doc.go  types.go  runcreate.go  dedupe.go  ack.go  volume.go  cron.go
internal/tasksource/inbox.go  poller.go  plugin.go  builtin.go  webhook.go  manager.go
internal/tasksource/testdata/trigger-demo.yaml
internal/tasksource/*_test.go (runcreate, volume, cron, inbox, poller, plugin, webhook, traceable_trigger)

internal/store/sqlite/migrations/0003_triggers.sql
internal/eventstore/triggers.go

internal/api/sources.go
internal/cli/src.go

internal/archtest/run_creation_reachable_test.go
internal/archtest/fixtures/runcreationreachable/violation.go

# modified:
internal/api/runs.go  server.go  routes_test.go
internal/apispec/ops.go
internal/cli/client.go  root.go
internal/config/config.go
internal/domain/event.go
internal/events/init.go  registry.go  fixtures_test.go  registry_test.go
internal/eventstore/store.go
cmd/kairos/serve.go
11-limitations.md
```

## Data changes

Three new tables in the existing `kairos.db`, migration `0003_triggers.sql`: `source`,
`source_cursor`, `trigger_dedupe` — verbatim from 08-triggers.md's own schema. One additive domain
event, `secret.accessed`, on the existing `"system"` stream (no new stream type).

## Acceptance criteria

- `go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run` clean; `go test ./... -race`
  green across every package, including the full `cmd/kairos` real-binary suite.
- All architecture tests pass, including the newly-real `TestArchitecture_runCreationNotReachableFromActors`
  (with its own violation fixture) and `TestUI_everyCallHasCLICounterpart` (covering the four new
  `/sources*` routes and their CLI verbs).
- `TestEngine_everyRunHasATraceableTrigger` passes for the CLI, poll, and cron paths, checked
  directly against the durable log's first event, not in-memory state.
- The dedupe race is proven, not asserted: `TestTriggerRun_concurrentSameKeyProducesExactlyOneRun`
  runs 10 concurrent callers under `-race` and confirms exactly one creates a run.
- The inbox's three real behaviours are each proven against a real filesystem watcher: one file → one
  run, rapid rewrites within the quiet period collapse to one pickup, identical content dropped twice
  is a no-op landing in `.dup/`.
- The plugin contract is proven against a real subprocess (a `/bin/sh` script), not a mock: describe/
  poll round-trip, secrets arrive as env vars and never in the request body (with a real
  `secret.accessed` event recorded), and a non-zero exit with no JSON normalises to a typed internal
  error.
- The webhook handler is proven against real HMAC-SHA256 signatures: a bad signature is rejected
  with no distinguishing body, a valid one creates a run, and three redeliveries of the same payload
  produce exactly one run.
- `make cross` builds `CGO_ENABLED=0` for darwin/linux × arm64/amd64.
- No `TODO`, `FIXME`, or commented-out code in the diff.

## Tests

- `internal/tasksource/runcreate_test.go`: `TriggerRef` propagation, `ValidationError` vs. store
  errors, `maxQueued` rejection.
- `volume_test.go`: passthrough, debounce collapse, digest batching, rate-triggered degrade.
- `cron_test.go`: `Daily`/`Weekly` next-fire correctness, `catchUp: skip` (no backlog after a 19-day
  gap), no false cold start on normal cadence.
- `inbox_test.go`: one file → one run, rewrite-collapse, content-hash dedupe → `.dup/`.
- `poller_test.go`: item → run + cursor persistence, invalid item skipped (not panicked), rejection
  → `ack(outcome: rejected)`.
- `plugin_test.go`: real-subprocess describe/poll, secrets-as-env-vars + `secret.accessed`, non-zero
  exit normalisation.
- `webhook_test.go`: bad signature, valid signature, triple-redelivery dedupe.
- `traceable_trigger_test.go`: `TestEngine_everyRunHasATraceableTrigger`.
- `internal/archtest/run_creation_reachable_test.go`: `TestArchitecture_runCreationNotReachableFromActors`,
  with its own violation fixture.
- `internal/api`: `routes_test.go`'s `nopStore` extended to the four new `Store` methods; existing
  `TestCreateRun_crashBetweenTheTwoAppendsLeavesOnlyTriggerReceived` kept passing through the
  `ValidationError`-vs-internal-error status-code split this document introduced.

## Benchmarks

None. Trigger volume for a single-user daemon is orders of magnitude below where the SQLite
single-writer connection or the dedupe INSERT would show up in a profile.

## Migration

`0003_triggers.sql` is additive and forward-only, applying `sqlite.Migrate`'s existing backup-before-
migrate discipline (unchanged from L02).

## Future work

- Real `github`/`jira`/`linear` `TaskSource` providers (NL-41).
- Stream-mode plugin invocation for webhook-fed sources (NL-42).
- Live pause/resume without a daemon restart (NL-43) — a `context.CancelFunc` registry inside
  `Manager` keyed by source id.
- Full cron(5)-expression scheduling, once a real need justifies the new dependency's ADR (NL-44).
- `repo-watch` (NL-45) — needs `internal/workspace` integration and a test-failure-detection trigger
  this document did not build.
- `kairos src add`'s `--config` flag currently takes raw JSON on the command line; a friendlier
  per-kind flag surface (e.g. `--repo`/`--label` for `github-issues`) is cosmetic, deferred.
