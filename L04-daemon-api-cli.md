# L04 — Daemon: API + SSE + CLI

## Depends on

L03 (definition + validator), transitively L02 (event store) and L01 (domain model). This is the
first document to give `internal/eventstore` and `internal/registry` a real consumer — everything
through L03 was proven by tests alone.

## Scope

**In.**
- `internal/apispec`: a zero-dependency `Op{Method, Path, CLIVerb}` table both `internal/api` and
  `internal/cli` independently consult.
- `internal/eventstore`: `ListRuns`/`GetRunState` added to `Store`, reading `run_index`/
  `run_state_projection` — thin SELECTs, no new SQL-importer.
- `internal/api`: the daemon's HTTP route table over a unix socket — `POST /runs`, `GET /runs`,
  `GET /runs/{id}`, `GET /events` (SSE), `GET /status`, `GET /doctor`, `POST /db/verify`, `POST
  /db/rebuild`. `internal/api.Listen` binds and chmods the socket to `0600`.
- `internal/cli`: cobra verbs (`version`, `serve`, `run`, `ls`, `show`, `doctor`, `db verify`,
  `db reindex`, `status`), a unix-socket `http.Client`, and daemon auto-start via an injected
  `DaemonStarter`.
- `cmd/kairos`: rewritten as the composition root — `main.go` wires `internal/cli`'s root command;
  `serve.go` is the daemon boot sequence (the only place besides `internal/executor/local` — which
  doesn't exist yet — permitted `os/exec`/`syscall`, and the only place besides `internal/api`
  itself permitted to import `internal/api`); `daemonstart_exec.go` is the real `DaemonStarter`.
- Two updated architecture tests (`noExecOutsideExecutor`, `dependencyDirection`) and one new one
  made real (`TestUI_everyCallHasCLICounterpart`).
- `adr/0012-daemon-lock-without-flock.md` (status `Proposed`); two new `11-limitations.md` entries
  (NL-26, NL-27) and NL-07 updated to reflect what actually shipped.

**Out.** The engine's dispatch loop, replay, reconciliation (L05); the TUI (L15); the web UI
(L20); `--wait` (needs a run to reach a terminal state, which needs L05); `kairos logs --follow`
as its own verb; `SO_PEERCRED` (NL-26); real `flock` (ADR 0012).

## Documented decisions

1. **`POST /runs` performs one synchronous `domain.Advance` fold beyond `TriggerReceived`, to also
   append `RunStarted`.** Not the engine: there is no dispatch loop, no per-run goroutine shard, no
   `Cmd` execution — the returned `[]Cmd` is computed and deliberately discarded, exactly the fold
   `RunStateProjection` already performs inside `AppendIf`'s own transaction. L05's reconciliation
   later re-derives identical state from the log alone.
2. **No real `flock` for `daemon.lock`.** A PID file (`O_CREATE|O_EXCL`) plus a socket-dial
   liveness probe detects and clears a stale lock. See [ADR 0012](adr/0012-daemon-lock-without-flock.md).
3. **No `SO_PEERCRED` peer-credential check.** Filesystem permissions alone (`0700`/`0600`,
   `TestListen_bindsAt0600`). Registered as NL-26, not silently accepted.
4. **`cmd/kairos` gets narrow, distinct exemptions**: from `noExecOutsideExecutor` (starting the
   daemon itself, and the doctor toolchain checks, are the binary bootstrapping its own second
   role — not a workflow actor process) and from `dependencyDirection`'s "nothing imports
   internal/api" rule (someone has to wire the HTTP handlers together; `cmd/kairos` is the
   composition root, the same posture it already holds for `os.Exit`). `internal/cli` itself
   imports neither `os/exec`/`syscall` nor `internal/api` — the real daemon-spawn and
   daemon-serve implementations live in `cmd/kairos`, injected into `internal/cli` as a
   `DaemonStarter` and a `ServeFunc`.
5. **`internal/apispec`** keeps `internal/api` and `internal/cli` mutually unaware of each other's
   existence while giving `TestUI_everyCallHasCLICounterpart` something concrete to walk, real
   from L04 rather than retrofitted at L20.
6. **`kairos doctor`'s toolchain-presence checks (`git`/`gh` on `PATH`) run once at daemon boot**
   inside `cmd/kairos serve`'s bootstrap and are cached; `GET /doctor` only reads the cached slice
   — `internal/api` itself never calls `exec.LookPath`.
7. **`TestArchitecture_agentSocketRouteSubset` stays skipped**, its citing message corrected: the
   narrow agent-facing socket (`check-output`/`artifact stage`/`ask-human`) is what actors call
   once actors exist (L08), a different socket from L04's admin-facing daemon socket.
8. **SSE resumption**: subscribe to the live bus *before* replaying history via `ReadAll`, then
   drain the live channel dropping anything with `GlobalSeq` already covered by the replay —
   closes the gap/duplicate window without a lock. Proven by
   `TestSSE_resumptionHasNoGapOrDuplicate`, which appends an event concurrently with an in-flight
   replay and asserts every `global_seq` is delivered exactly once.
9. **The `TriggerReceived`/`RunStarted` split is two separate `AppendIf` calls, not one
   transaction.** A crash between them leaves a run at `TriggerReceived` only — proven
   deterministically by `TestCreateRun_crashBetweenTheTwoAppendsLeavesOnlyTriggerReceived`
   (a store wrapper that fails the second call), not by racing a real `kill -9`. Registered as
   NL-27 and a named Future-work item for L05.

## Public interfaces

```go
// internal/apispec
type Op struct { Method, Path, CLIVerb string }
var Ops []Op

// internal/eventstore, added to Store
ListRuns(ctx context.Context, status *domain.RunStatus) ([]RunSummary, error)
GetRunState(ctx context.Context, runID string) (domain.RunState, bool, error)

// internal/api
type Deps struct { Store eventstore.Store; DoctorChecks []DoctorCheck; Deferred []string; StartedAt time.Time }
func NewMux(deps Deps) *http.ServeMux
func Listen(path string) (net.Listener, error)

// internal/cli
func Execute(args []string, starter DaemonStarter, serve ServeFunc) int
func RootCommand() *cobra.Command // introspection only, for the parity test
type DaemonStarter interface { Start(ctx context.Context) error }
type ServeFunc func(ctx context.Context) error
```

## Files to create

```
internal/apispec/ops.go

internal/eventstore/query.go  query_test.go

internal/api/server.go  respond.go  runs.go  events.go  status.go  doctor.go  db.go
internal/api/server_test.go  runs_test.go  events_sse_test.go  routes_test.go  crash_gap_test.go

internal/cli/doc.go  root.go  client.go  daemonstart.go  serve.go
internal/cli/run.go  ls.go  show.go  doctor.go  db.go  status.go  output.go

cmd/kairos/main.go  serve.go  daemonstart_exec.go  integration_test.go

adr/0012-daemon-lock-without-flock.md

# modified:
internal/archtest/no_exec_outside_executor_test.go
internal/archtest/dependency_direction_test.go
internal/archtest/deferred_test.go
internal/archtest/ui_cli_parity_test.go  (new)
11-limitations.md
adr/README.md
```

## Data changes

None beyond L02's schema. `daemon.lock` and `daemon.sock` are new files under `$KAIROS_HOME`,
not database rows.

## Acceptance criteria

- `go build ./...`, `go vet ./...`, `golangci-lint run` clean; `go test ./... -race` green (86
  tests passing across the repo).
- All nine architecture tests pass on a clean test-cache run, including the two-entry
  `noExecOutsideExecutor` exemption table, the `dependencyDirection` `cmd/kairos` carve-out, and
  the newly-real `TestUI_everyCallHasCLICounterpart`.
- `TestIntegration_runShowListVerify` (`cmd/kairos`) builds the real binary, runs `kairos run
  testdata/fix-issue.yaml` against a temp `$KAIROS_HOME` with no daemon running (auto-start),
  and confirms `kairos show`/`kairos ls`/`kairos db verify` all reflect it correctly.
- `TestIntegration_daemonSurvivesASecondAutoStartAttempt` proves the PID-file lock refuses a
  second daemon rather than silently running two.
- `TestSSE_resumptionHasNoGapOrDuplicate` and
  `TestCreateRun_crashBetweenTheTwoAppendsLeavesOnlyTriggerReceived` prove decisions #8 and #9
  precisely, not just in prose.
- `make cross` builds `CGO_ENABLED=0` for darwin/linux × arm64/amd64, including the new
  `os/exec`/`syscall` code in `cmd/kairos`.
- No `TODO`, `FIXME`, or commented-out code in the diff.

## Tests

- `internal/eventstore/query_test.go`: `ListRuns` filtering, `GetRunState` found/not-found.
- `internal/api`: route registration against `apispec.Ops`, `POST /runs`'s full fold (asserting
  both events land), validation-failure → 422, not-found → 404, listing, SSE resumption, socket
  permissions, the crash-gap proof.
- `cmd/kairos/integration_test.go`: the two real end-to-end tests described above.
- `internal/archtest/ui_cli_parity_test.go`: every `apispec.Op` maps to a CLI verb and vice versa.

## Benchmarks

None. Nothing here is on L02's durability-sensitive hot path; the daemon boot sequence and HTTP
handlers are not benchmarked at this layer.

## Migration

None from a prior version.

## Future work

- L05 (engine) is the real dispatch consumer: it reads `Cmd`s `domain.Advance` returns (which
  L04 computes once and discards) and turns them into executor/workspace/gate actions. Its
  reconciliation loop must recognise and handle the `TriggerReceived`-only stuck-run state NL-27
  registers.
- L06 (`internal/executor/local`) is the revisit trigger for ADR 0012 — once it exists, the
  daemon lock can move from a PID file to a real `syscall.Flock` behind a one-function-call
  change.
- L08 (actor SDK) specifies the narrow, agent-facing socket `TestArchitecture_agentSocketRouteSubset`
  will finally implement — a different socket from L04's admin-facing one.
- L15 (TUI) changes bare `kairos` (no arguments) from "ensure daemon, print status" to "attach the
  TUI if stdout is a TTY, else print status" — a one-line change to `runStatus`'s caller in
  `root.go`.
- L20 (web UI) extends `apispec.Op` with a `WebCall` dimension, reusing the parity discipline
  `TestUI_everyCallHasCLICounterpart` already establishes rather than retrofitting it.
- `kairos logs --follow` as its own verb (the SSE plumbing already exists via `GET /events`) is
  deferred — noted here rather than silently dropped.
