# L00 — Bootstrap

## Depends on

Nothing. This is the first build document.

## Scope

**In.**
- The Go module, repository skeleton, and the empty-but-real packages whose import
  boundaries later documents must not cross: `internal/domain`, `internal/executor/local`,
  `internal/store/sqlite`, `internal/tui`, `internal/api` — each a `doc.go` stating the law
  it upholds, no logic.
- `internal/version` (build identity) and `internal/config` (resolve and create
  `$KAIROS_HOME`, respecting `$XDG_STATE_HOME`) — the minimum config surface `main.go`
  and every later document need, not the full `config.yaml` schema from `02-config.md`.
- `cmd/kairos/main.go` with one verb, `kairos version`, wired directly with cobra — the
  only legal `os.Exit`/`log.Fatal` site in the tree.
- Six of the nine architecture tests from `01-architecture.md`, built as AST/import-graph
  checks over `golang.org/x/tools/go/packages`, each with a `//go:build violation` fixture
  that the same test asserts the checker catches: `domainPurity`, `noExecOutsideExecutor`,
  `tuiHasNoExecution`, `dependencyDirection`, `noSQLOutsideStore`, `noOsExitOutsideMain`.
- The remaining three architecture tests (`singleWriter`, `processesRecordedBeforeSpawn`,
  `agentSocketRouteSubset`) exist as named, `t.Skip`ped tests naming the document that
  will implement them, so `go test ./...` enumerates all nine from day one without
  faking a check that has nothing real to check yet.
- CI (GitHub Actions): build, vet, race-enabled unit tests, `CGO_ENABLED=0` cross-builds
  for darwin/linux × arm64/amd64, `golangci-lint`, `make arch`.
- `Makefile` with `build`, `test`, `race`, `arch`, `lint`, `tidy`, `cross`, `clean`.

**Out.**
- Migrations, `internal/store/sqlite`'s `Open`/`Migrate`, and `embed.FS` — these belong to
  L02 (event store), sequenced after L01 establishes what the store persists. The mermaid
  build order (`L00 → L01 → L02`) is authoritative over the phase-0 day-table's grouping of
  "bootstrap, config, migrations" into one effort estimate.
- `internal/cli` — not created; `main.go` wires cobra directly for its one verb. Extracted
  once verb count actually grows (L04 onward).
- `internal/engine`, `internal/eventstore`, `internal/registry`, `internal/workspace`,
  `internal/admission`, `internal/connector`, `web/` — none of L00's acceptance criteria
  need them.
- `TestEngine_everyRunHasATraceableTrigger` — needs `internal/domain` events and
  `internal/engine`, neither scaffolded here. Picked up at L05.
- Any `serve`, `run`, `doctor`, `ls`, `show`, `inbox` CLI verb — each depends on packages
  this document does not create.
- `pkg/` — ships empty per AGENTS §7; nothing to move into it yet.

## Public interfaces

```go
// internal/version
func String() string

// internal/config
type Config struct{ Home string }
func Load() (Config, error)
```

No other exported surface. The five empty packages export nothing.

## Files to create

```
go.mod  go.sum
Makefile  .golangci.yml  .gitignore
.github/workflows/ci.yml
L00-bootstrap.md

cmd/kairos/main.go
cmd/kairos/main_test.go

internal/version/version.go

internal/config/config.go
internal/config/config_test.go

internal/domain/doc.go
internal/executor/local/doc.go
internal/store/sqlite/doc.go
internal/tui/doc.go
internal/api/doc.go

internal/archtest/helpers.go
internal/archtest/domain_purity_test.go
internal/archtest/no_exec_outside_executor_test.go
internal/archtest/tui_has_no_execution_test.go
internal/archtest/dependency_direction_test.go
internal/archtest/no_sql_outside_store_test.go
internal/archtest/no_os_exit_outside_main_test.go
internal/archtest/deferred_test.go
internal/archtest/fixtures/domainpurity/violation.go
internal/archtest/fixtures/noexecoutsideexecutor/violation.go
internal/archtest/fixtures/tuihasnoexecution/violation.go
internal/archtest/fixtures/dependencydirection/violation.go
internal/archtest/fixtures/nosqloutsidestore/violation.go
internal/archtest/fixtures/noosexitoutsidemain/violation.go
```

## Data changes

None. No `~/.kairos/kairos.db` is created or touched by this document — `config.Load`
creates the `$KAIROS_HOME` directory only. The SQLite file is L02.

## Acceptance criteria

- `go build ./...`, `go vet ./...` are clean; `go test ./... -race` is green.
- `CGO_ENABLED=0` builds succeed for darwin/linux × arm64/amd64 (`make cross`).
- `golangci-lint run` is clean with the repo config.
- `make arch` passes, and every one of the six real architecture-test files contains a
  subtest asserting its checker **fails** against its own `//go:build violation` fixture —
  demonstrated by the subtest itself passing (i.e., the checker did detect the violation),
  not by a manual CI toggle.
- `kairos version` prints a version string and exits 0.
- `config.Load()` creates `$KAIROS_HOME` at mode `0700`, honours `$KAIROS_HOME` and
  `$XDG_STATE_HOME` overrides, and is covered by a test using a real filesystem in
  `t.TempDir()` — no ambient `$HOME` dependence.
- `TestArchitecture_singleWriter`, `TestArchitecture_processesRecordedBeforeSpawn`, and
  `TestArchitecture_agentSocketRouteSubset` exist, are named exactly as in AGENTS §9, and
  are skipped with a message naming the document that will implement them.
- No `TODO`, `FIXME`, or commented-out code in the diff.

All of the above verified locally: `go build ./...`, `go vet ./...`, `go test ./... -race`,
`golangci-lint run`, and `make cross` all pass; `go test ./internal/archtest/... -v` shows
each of the six real checks running both its `realTree` and `fixtureIsCaught` subtests to a
pass, and the three deferred checks skipping with their citing message.

## Tests

- `internal/config`: default resolution, `KAIROS_HOME` override, `XDG_STATE_HOME`
  override, directory permissions, idempotent re-`Load`.
- `internal/archtest`: as described above — one real-tree assertion and one
  fixture-must-trip assertion per check, for all six implemented checks.
- `cmd/kairos`: an integration test (`main_test.go`) that builds the real binary via
  `exec.Command` and asserts `kairos version` exits 0 and prints a non-empty string. This
  lives in an external `_test` package and is not scanned by `go/packages` in default mode
  (no `Tests: true`), so it does not trip `TestArchitecture_noExecOutsideExecutor` — it is a
  test invoking a binary, not application code spawning a process.

## Benchmarks

None. Nothing performance-sensitive exists yet — no store, no executor. The first
benchmark gate (`BenchmarkAppendIf_singleEvent < 5ms p99`) is L02's.

## Migration

None. No prior schema, no prior binary.

## Future work

- L01 fills `internal/domain` with real types and state machines; `domainPurity`'s
  `time.Now` check and the "imports nothing from `internal/`" check start exercising real
  code instead of an empty package.
- L02 adds `internal/store/sqlite`'s `Open`/`Migrate`/`migrations/*.sql` (embed.FS,
  forward-only) and turns on the single-writer goroutine, at which point
  `TestArchitecture_singleWriter`'s skip is replaced with a real check. It also adds
  `modernc.org/sqlite` to the forbidden-import list in `noSQLOutsideStore`, alongside
  `database/sql`.
- L04 gives `internal/api` its route table and `agentSocketRouteSubset` a real subset to
  assert against; also the point where `internal/cli` is worth extracting from `main.go`.
- L06 gives `internal/executor/local` its `Start`/`Signal` and
  `processesRecordedBeforeSpawn` a real "recorder call precedes every `cmd.Start`" AST
  check. It also adds `golang.org/x/sys/unix` to `noExecOutsideExecutor`'s forbidden list.
- L05 is where `TestEngine_everyRunHasATraceableTrigger` first becomes writable at all.
- `dependencyDirection`'s forbidden-edge table is expected to grow with every later
  document that introduces a new internal package; each addition is a one-line table entry
  plus a fixture, not a rewrite of the check.
