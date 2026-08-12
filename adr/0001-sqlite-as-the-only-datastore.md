# 0001 — SQLite as the only datastore

**Status:** Accepted · supersedes the ancestor design's "Postgres as the event store"
**Date:** 2026-08-12

## Context

Kairos is one binary that a user opens on their own machine. The ancestor design chose PostgreSQL and
rejected SQLite on four grounds, all of which were correct *for a multi-process control plane*: no
concurrent writers, no `LISTEN/NOTIFY`, no path to HA or a second control-plane instance, and weaker JSON
support. Each of those objections has to be answered rather than ignored, because three of them describe
real properties of SQLite and one of them describes a goal we are abandoning on purpose.

## Decision

Embedded SQLite in WAL mode at `~/.kairos/kairos.db` is the only datastore. No external database, no
server to run, no connection string. Log bytes live on the filesystem, not in the database.

Driver: **`modernc.org/sqlite`** (pure Go, no cgo), imported from exactly one file.

Mandatory settings, in the document rather than in folklore: `journal_mode=WAL`,
`synchronous=FULL` + `fullfsync=1` (Darwin), `foreign_keys=ON` *per connection*, `busy_timeout=5000`, and
`_txlock=immediate` on the single writer connection. `STRICT` tables throughout.

The four objections, answered:

- **No concurrent writers** — this is the design, not a limitation. One writer goroutine owns the write
  connection and group-commits up to 64 appends per fsync. `SQLITE_BUSY` becomes unreachable on the write
  path, and the entire contention story disappears.
- **No `LISTEN/NOTIFY`** — irrelevant, and the replacement is strictly better. The only subscribers are
  in-process, so fan-out is a function call after commit: it cannot drop, has no payload limit, and
  delivers in commit order. The ancestor design called notify "an optimisation over polling"; here it is
  the only mechanism *and* it is sound.
- **No path to HA** — abandoned deliberately. See [0002](0002-one-process-one-host.md).
- **Weaker JSON** — `json1` is compiled in everywhere; `STRICT` plus `CHECK (json_valid(payload))` plus
  application-level JSON Schema validation covers the need. The one real loss is a GIN index over
  payloads, replaced by `VIRTUAL` generated columns with b-tree indexes on the three paths that are
  actually queried.

## Consequences

**Good.** One file to back up, with `VACUUM INTO` giving an atomic hot consistent copy. `t.TempDir()` is a
disposable real instance, so integration tests need no containers and run in `make verify`. Projections
apply in the same transaction as the append, so projection lag is structurally zero and the API has
read-your-writes. SQL for ad-hoc analysis of your own history.

**Bad.** A pure-Go SQLite is a C-to-Go translation: roughly 1.5–3× slower on writes and ~8–10 MB of
binary. The single `.db` file is a single point of failure with no replica. Forward-only migrations, with
the recovery path being "restore the pre-migration backup" — which is at least testable, unlike a down
migration nobody ever runs. `~/.kairos` on a cloud-synced or network-mounted home risks real corruption,
so startup refuses a non-local filesystem.

## Alternatives considered

- **`mattn/go-sqlite3`** — faster and smaller, and rejected because it makes cross-compilation a project.
  `CGO_ENABLED=0 go build` for darwin and linux × arm64 and amd64, from one machine, in one
  `make release`, with no clang, is worth more to a tool whose entire pitch is "one binary" than a write
  path that is already three orders of magnitude faster than needed. It also keeps `go install …@latest`
  working on a machine with no C compiler, and keeps `-race` output on a goroutine-heavy event bus
  readable.
- **Keeping Postgres, optionally** — two storage backends to keep correct, for a single user, to serve a
  topology that [0002](0002-one-process-one-host.md) rules out.
- **A bespoke append-only file format** — loses SQL, transactional projections, and forty years of
  crash-correctness work.

## Revisit when

`BenchmarkAppendIf_singleEvent` exceeds **10 ms p99 sustained** (the CI gate is 5 ms). The driver is
imported from one file and guarded by `TestArchitecture_noSQLOutsideStore`, so switching to
`mattn/go-sqlite3` behind a build tag is a one-file change rather than a refactor — that is the whole
reason the constraint exists.
