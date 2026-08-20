# L09 — Artifacts + logs

## Depends on

L06 (local executor + workspaces), transitively L05→L04→L03→L02→L01→L00. All satisfied.

## Scope

**In.**
- `internal/artifact`: a content-addressed blob store rooted at `$KAIROS_HOME/artifacts` —
  `blobs/sha256/<first-2-hex>/<full-hex>` — with `PutBytes` (for in-memory payloads) and `Put`
  (collects an existing file via `rename(2)`, "not a copy", per 06-durability.md), both
  deduplicating by content hash.
- Log rotation: `artifact.CollectLog(path)` zstd-compresses a node's `stdout.log`/`stderr.log` to
  `<path>.zst` and removes the original, once it has grown past the 64 MiB threshold
  06-durability.md names — run once, at node-completion, from `internal/engine`'s
  `reapShell`/`reapLLM`.
- Payload-size discipline: `internal/engine.storeOutput` inlines a `NodeOutputReceived` body under
  8 KiB (06-durability.md's threshold) exactly as before L09, and above it stores the body in the
  artifact store and records an `ArtifactRef` (hash + size) instead — additive to
  `domain.NodeOutputReceived` via a new `OutputRef *ArtifactRef` field.
- Two additive, run-scoped `internal/domain` events — `LogDegraded`, `LogTruncated` — recorded when
  log rotation cannot proceed safely, following L05/L06/L08's exact schema+fixture+registry
  pattern.
- `github.com/klauspost/compress/zstd` added as a dependency (already on AGENTS.md's approved
  table, pure Go, no cgo — confirmed against the `CGO_ENABLED=0` cross-compile gate).

**Out.** Artifact **redaction** (the transcript/stdout.log secrets-scrubbing pass,
`artifact.redacted{count}`) — registered as NL-32: until it ships, the artifact store must be
treated as containing secrets. The `kairos check-output`/`artifact stage` agent-facing CLI socket
surface (L08's narrow concern, unchanged here). Gates (L10). Effects (L12). A SQLite index row per
log file (06-durability.md mentions one; this document's 1-day budget scopes to the filesystem
layout and rotation mechanism alone — see Documented decisions #3 and Future work). Live rotation
of a log still being actively written (see decision #2).

## Documented decisions

1. **Stdout/stderr capture-to-file already existed (L05); L09's job was rotation, not
   creation.** `internal/executor/local/spawn.go`'s `Start` already opens `stdout.log`/
   `stderr.log` as real files (never pipes — a hard constraint L06's process-adoption durability
   guarantee depends on, 06-durability.md: "pipes die with their reader") and hands them to the
   child directly. L09 adds compression-on-rotation on top of that existing capture path; it does
   not touch `internal/executor/local` at all.
2. **Log rotation happens once, at node-execution completion — never while the child is actively
   writing.** The child holds a real, non-pipe file descriptor to `stdout.log`/`stderr.log` for its
   entire execution (decision #1's constraint). Truncating, renaming, or replacing that file out
   from under a live writer's fd would corrupt the stream: `write(2)` continues at the fd's current
   offset regardless of what a separate process does to the path, so an external rotation mid-write
   either pads the file with zero bytes or silently loses bytes, and neither is acceptable under
   "block first, degrade second, never silently." `CollectLog` therefore runs from
   `reapShell`/`reapLLM`, after `Wait` returns, on a log the process has already stopped writing to.
   A multi-GB single-node-execution log that needs to rotate *during* a still-running node (a real
   scenario for a very long agent session) needs fd-recycling machinery (e.g. reopen-on-SIGHUP or a
   daemon-side tee) this document does not build — named explicitly in Future work rather than
   silently unhandled.
3. **No SQLite index row for rotated/compressed logs.** 06-durability.md names "an index row in
   SQLite" for logs; adding that means a new migration and a new table, disproportionate to this
   document's 1-day budget and not required for correctness — `kairos verify`/replay depend only on
   the SQLite event log, never on logs (06-durability.md's own invariant: "deleting `logs.db`
   entirely must leave every run replayable" — there is no `logs.db`, logs are files, and nothing
   in the replay path reads them). A log's location remains discoverable by the existing
   `WorkRoot/<runID>/<execID>/{stdout,stderr}.log[.zst]` path convention alone. A query index (for
   a future "list all logs over N MiB" or similar admin surface) is Future work.
4. **`OutputRef *ArtifactRef` is an additive field on the existing `NodeOutputReceived` v1 schema,
   not a new event version.** `node.output.received`'s v1 JSON Schema has no
   `additionalProperties: false`, so a new optional property validates against it without a schema
   bump — "add fields, never remove or repurpose" (AGENTS §4 rule 6) is satisfied by the field
   addition alone; every historical v1 fixture still projects unchanged (`Output` absent from
   `OutputRef`-bearing events is legal; `OutputRef` absent from every pre-L09 fixture is equally
   legal). `Output` and `OutputRef` are mutually exclusive by construction in
   `Engine.storeOutput` — never both set on the same event.
5. **`LogDegraded`/`LogTruncated` fold as no-op audit facts**, exactly like L08's
   `LLMSessionStarted`/`SessionResumeFailed`/`SessionCostUnavailable`/`OutputRepairAttempted` —
   added to the same `advance.go` case rather than a new one, since they share the identical shape
   (`RunID`/`NodeID`/`ExecID`-bearing, run-scoped, no effect on `ExecStatus` or routing).
6. **Backpressure is scoped to the rotation subsystem, not a live log reader.** 06-durability.md's
   "block first, degrade second" backpressure policy is written for a *live* SSE tail of a growing
   log (a reader blocking on a full channel, `log.degraded` at >5s blocked, `log.truncated` past a
   hard cap). No such live-tailing consumer exists yet — `kairos logs --follow` was named and
   explicitly deferred back in L04's decisions, and nothing built since has changed that. Building
   a real streaming reader with backpressure semantics to satisfy a policy with no consumer would
   be speculative scope (AGENTS §7). What L09 *does* implement faithfully is the same "never
   silently" discipline applied to the one place a log write can genuinely fail today —
   node-completion rotation: a compression failure (a full disk, a permission error) is recorded as
   `LogDegraded` rather than swallowed or panicking, and the original log is left completely
   untouched on any failure (proven by `TestCollectLogs_rotationFailureRecordsLogDegraded`). Full
   live-tailing backpressure is named in Future work, gated on a live log-reading feature actually
   existing to backpressure.
7. **No `ArtifactRoot` config knob was added.** Following `MirrorRoot`'s existing precedent (L06):
   `engine.Config.ArtifactRoot` defaults to an `artifacts` sibling of `WorkRoot`'s parent
   (`$KAIROS_HOME/artifacts`) with no environment-variable override and no change to
   `cmd/kairos/serve.go`, matching `MirrorRoot`'s identical zero-wiring convention rather than
   introducing an inconsistent new knob for symmetry's sake alone.

## Public interfaces

```go
// internal/artifact
type Ref struct { Hash string; Size int64 }
func New(root string) *Store
func (s *Store) Put(srcPath string) (Ref, error)       // rename(2) collection, dedups
func (s *Store) PutBytes(data []byte) (Ref, error)      // in-memory payload, dedups
func (s *Store) Path(ref Ref) string

const RotateThreshold = 64 * 1024 * 1024
func CollectLog(path string) (rotated bool, err error)

// internal/domain, additive
type ArtifactRef struct { Hash string; Size int64 }
type LogDegraded struct { RunID, NodeID, ExecID, Stream, Reason string }
type LogTruncated struct { RunID, NodeID, ExecID, Stream string; DroppedBytes int64 }

// NodeOutputReceived, additive field
type NodeOutputReceived struct {
    RunID, NodeID, ExecID string
    SchemaValid           bool
    Output                json.RawMessage // nil when OutputRef is set
    OutputRef             *ArtifactRef    // nil when Output is set
}

// internal/engine.Config, additive field
ArtifactRoot string // defaults to $KAIROS_HOME/artifacts
```

## Files to create

```
internal/artifact/store.go  logrotate.go
internal/artifact/store_test.go  logrotate_test.go

internal/events/schemas/log.degraded/v1.json  internal/events/fixtures/log.degraded/v1.json
internal/events/schemas/log.truncated/v1.json  internal/events/fixtures/log.truncated/v1.json

internal/engine/logs.go  logs_internal_test.go

# modified:
internal/domain/event.go  advance.go
internal/events/init.go  registry.go  fixtures_test.go  registry_test.go
internal/engine/engine.go  actor_shell.go  actor_llm.go
11-limitations.md  (NL-32)
go.mod  go.sum  (github.com/klauspost/compress)
```

## Data changes

None to the SQLite schema — see decision #3. New on-disk layout under `$KAIROS_HOME`:
`artifacts/blobs/sha256/<aa>/<hash>` (content-addressed blobs) and, per node-execution directory,
an optional `stdout.log.zst`/`stderr.log.zst` replacing the plain `.log` file once it exceeds
64 MiB.

## Acceptance criteria

- `go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run` clean; `go test ./... -race`
  green across every package, including the two new packages/test files.
- All nine architecture tests pass unchanged — `internal/artifact` imports neither `os/exec` nor
  `database/sql`, so no exemption list needed updating.
- Writing the same content twice into the artifact store dedups: one blob on disk, verified by
  `TestStore_putBytesDedupsIdenticalContent` and `TestStore_putCollectsViaRenameAndDedups`.
- `rename(2)`-based collection is atomic under concurrent readers:
  `TestStore_putIsAtomicUnderConcurrentReaders` runs 20 concurrent `Put` calls on identical 5 MiB
  content and asserts every resulting blob read is complete, never torn.
- A log exceeding 64 MiB rotates and zstd-compresses; a log under the threshold is left untouched;
  a missing log is a no-op — `TestCollectLog_aboveThresholdRotatesAndCompresses`,
  `TestCollectLog_belowThresholdLeavesFileUntouched`, `TestCollectLog_missingFileIsANoOp`.
- An oversized `NodeOutputReceived` body is stored as an `ArtifactRef`, never inlined; a small body
  stays inlined exactly as before L09 — `TestStoreOutput_oversizedBodyBecomesAnArtifactReference`,
  `TestStoreOutput_smallBodyStaysInline`.
- A rotation failure records `log.degraded` rather than a silent gap, panic, or crash, and leaves
  the original log byte-for-byte untouched — `TestCollectLogs_rotationFailureRecordsLogDegraded`.
- `make cross` builds `CGO_ENABLED=0` for darwin/linux × arm64/amd64, confirming
  `klauspost/compress/zstd` is genuinely cgo-free.
- No `TODO`, `FIXME`, or commented-out code in the diff.

## Tests

- `internal/artifact/store_test.go`: dedup on `PutBytes` and `Put`, rename-based atomic collection,
  concurrent-writer atomicity.
- `internal/artifact/logrotate_test.go`: rotation threshold behavior (above/below/missing),
  round-trip content integrity through zstd compression.
- `internal/engine/logs_internal_test.go` (white-box, `package engine`): `storeOutput`'s inline-vs-
  reference branch, `collectLogs`'s failure-path event recording (permission-denied injection,
  deterministic, no real full-disk simulation needed).
- `internal/events`: `TestBuiltin_registersAllTwentySixEventTypes` (was
  `...TwentyFourEventTypes`), `log.degraded`/`log.truncated` added to `fixtures_test.go`'s
  `projectFixture` scenario table.

## Benchmarks

None. Log rotation runs once per node execution at completion, off any hot path;
`BenchmarkAppendIf_singleEvent`'s existing CI gate is unaffected since `OutputRef` keeps oversized
payloads *out* of the event log, which is the entire point.

## Migration

None from a prior version. Existing `stdout.log`/`stderr.log` files from runs recorded before L09
are left exactly as they are — `CollectLog` only ever acts on a log at the moment its owning node
execution completes, never retroactively on old data, so nothing needs a backfill.

## Future work

- Real transcript/output artifact **redaction** (NL-32) — a scan-and-rewrite pass over
  content-addressed blobs before or after collection.
- Live rotation of a log still being actively written by a very-long-running single node execution
  (decision #2) — needs fd-recycling machinery not yet designed.
- A SQLite index row per log file (decision #3), if a future admin/query surface needs one.
- Live SSE log-tailing with real reader backpressure (`log.degraded` at >5s blocked,
  `log.truncated` past a hard cap) — gated on `kairos logs --follow` or an equivalent live-tailing
  feature actually existing (decision #6).
- `kairos show`/the web UI resolving an `OutputRef` back to its blob content for display — today an
  oversized output shows `Output: null` in `kairos show`'s JSON, and a caller must read the
  artifact store directly by hash.
- Artifact store GC (workspace GC already exists from L06; the blob store has none yet — part of
  NL-32's blast radius).
