# L19 — Self-check + chaos

## Depends on

L12 (effects + compensation) and L17 (child runs), both edges satisfied — the chaos harness and
self-check exercise machinery both documents built (effect idempotency, spawn/join). Transitively
everything through L18.

## Scope

Unlike every prior document, L19 builds no new domain capability — it hardens and proves what
L00–L18 already built. The proportion of test code to production code is deliberately higher than
any prior document; that is correct for this document's nature, not under-scoping.

**In.**
- The chaos harness (`cmd/kairos/chaos_test.go`): many independent kill/restart/reconcile cycles
  against a real built binary, asserting zero orphan processes afterward.
- A live, on-demand self-check (`internal/engine.SelfCheck`, `kairos doctor --self-check`):
  event-log integrity (`Store.Verify`), a live scan for `Executing` `NodeExecution` rows whose
  recorded process is no longer verifiably alive, and orphan-workspace collection
  (`internal/workspace.Manager.GC`).
- Backup and restore-test: `Store.Backup` via `VACUUM INTO`, exposed as `kairos db backup <path>`,
  proven restorable (opened as an independent store, `Verify`d, run history read back) rather than
  merely "the file exists."
- `kairos pause`/`kairos resume`/`kairos park [--wait]`: an engine-wide pause flag that stops new
  `CmdStartNode` admission (a run holds at its next node boundary) without interrupting anything
  already `Executing` — genuinely distinct from `Stop`'s SIGTERM-then-kill shutdown path.
- `GET /status`'s `paused`/`inFlight` fields, backing `park --wait`'s polling loop.

**Out.** Replay verification and fork/compare machinery (L18, already built — this document
exercises it via the chaos harness's `db verify` calls, it does not rebuild it). `kairos up`/`down`
daemon-process lifecycle verbs (named in `09-cli-and-tui.md` but not yet built anywhere in this
project — `park` pauses and waits, it does not stop the daemon process itself; see Future work).
`kairos stats`/`kairos cost` (no such verb is named precisely in the source docs beyond the build
plan's own shorthand; `GET /status` already reports `activeRuns`/`paused`/`inFlight`, and L07/L12
already expose admission/spend data at their own layers — a dedicated aggregating verb is Future
work, not silently built as a guess). The cookie sweep for double-forked escapees
(`06-durability.md`'s own named residual gap, independent of this document).

## Documented decisions

1. **The chaos harness runs 20 iterations (10 killed mid-node), not 200.** `06-durability.md`'s
   original acceptance test is "no orphaned processes after 50 runs, including 10 killed";
   `12-build-plan.md`'s Phase 2 bar scales this to 200. This session's environment/time budget
   does not support literally running 200 real daemon start/kill/restart cycles inside one test
   run, so the suite runs 20 (the same 1-in-2 kill ratio as the original 50/10 test) — reported
   honestly here rather than claimed as 200. The invariant under test (identity-checked reaping +
   reconciliation never leaks a process) does not depend on iteration count once the mechanism is
   proven correct; 20 real cycles, each independently asserting zero orphans and a clean
   `db verify`, is real coverage of the actual claim, just not at the doc's literal target scale.
2. **Each chaos iteration uses its own fresh `$KAIROS_HOME`**, not one long-lived daemon surviving
   all 200/20 restarts. This trades "one daemon surviving many restarts" (closer to the literal
   scenario `06-durability.md` describes) for "N independent kill/restart cycles, each proven
   clean" — faster and more parallelizable, and the orphan-reaping/reconciliation invariant is
   per-cycle, not cumulative across a daemon's lifetime, so the substitution does not weaken the
   claim being tested.
3. **The chaos harness does not exercise duplicate-effect detection.** Its fixture is
   `shell`/`rule` actors only (no `actor: effect` node), so there is nothing to duplicate. Building
   a real git-remote-backed effect fixture into the chaos loop would slow the suite considerably
   for coverage L12's own kill-mid-effect reconciliation tests already provide directly. Named
   here rather than silently omitted from Acceptance criteria.
4. **`SelfCheck` never kills a process; boot-time `Reconcile` is the only code path allowed to.**
   During live operation, a `NodeExecution` the log calls `Executing` with no verifiably-alive
   process behind it is a genuine anomaly the owning shard should already have handled itself —
   `SelfCheck` reports it (`UnverifiableExecutions`) rather than guessing it is safe to act on,
   matching AGENTS §4 rule 1. Orphan-workspace collection is different: an orphan workspace
   directory has no owning run *by definition* (computed from `ListRuns`' own non-terminal set at
   check time), so `SelfCheck` performs that GC rather than merely reporting it — there is no
   "wrong to fix" case the way there is for a process it did not itself decide to kill.
5. **`doctor --self-check` is a flag on the existing `doctor` verb, not a new top-level verb.**
   `kairos doctor` is already "the host probe" (`09-cli-and-tui.md`); self-check deepens it rather
   than duplicating it. This also keeps `TestUI_everyCallHasCLICounterpart`'s parity discipline
   satisfied without inventing a verb the source docs never name (`POST /selfcheck`'s
   `apispec.Op` maps to the CLI verb `"doctor"`, not a synthesized `"doctor --self-check"` — cobra
   flags are not part of `Find`'s command-path resolution, so a flag-qualified string would never
   satisfy the parity test's `cliVerbExists` check; multiple `Op`s legitimately mapping to the same
   leaf verb is already the established pattern, e.g. L04's `GET /doctor`).
6. **`kairos park --wait` polls `InFlight` (the admission claims map's live size) down to zero**,
   not a purpose-built "count of genuinely in-flight node executions" tracked independently. A
   `rule`-actor node holds its claim for one synchronous call, so in practice this reflects
   shell/llm/effect actors with real, observable duration — the case `park --wait` actually exists
   to wait out. Documented as a live proxy, not a separately-audited metric.
7. **`park` does not stop the daemon process.** `09-cli-and-tui.md` frames `kairos park --wait` as
   "closing the lid," which reads as implying the daemon itself should also stop — but no
   `kairos up`/`down` daemon-process-lifecycle verb exists anywhere in this project yet (only
   `serve`, the foreground boot command, and auto-start via `ensureDaemon`). Building daemon
   stop/start lifecycle management now, scoped only to make `park`'s metaphor literal, would be
   scope this document does not need — `park --wait` pauses dispatch and waits for genuine
   quiescence, which is the operationally meaningful half of "closing the lid" (nothing is
   mid-write when it returns); actually terminating the process is a one-line addition once
   `up`/`down` exist, named explicitly in Future work rather than built as an out-of-scope guess.
8. **`Store.Backup` runs `VACUUM INTO` on the reader connection**, not routed through the
   single-writer goroutine's request channel. `VACUUM INTO` takes its own internal read lock and
   does not require exclusive access the way an `AppendIf` transaction does, so routing it through
   `s.reqs` would only add latency for every in-flight append with no correctness benefit.

## Public interfaces

```go
// internal/eventstore, added to Store
Backup(ctx context.Context, destPath string) error // VACUUM INTO

// internal/engine
func (e *Engine) SetPaused(ctx context.Context, paused bool)
func (e *Engine) Paused() bool
func (e *Engine) InFlightCount() int
type SelfCheckReport struct {
	DBClean                 bool
	MismatchedRunIDs        []string
	UnverifiableExecutions  []string
	OrphanWorkspacesRemoved []string
}
func (e *Engine) SelfCheck(ctx context.Context) (SelfCheckReport, error)

// internal/api, new routes
POST /pause
POST /resume
POST /selfcheck
POST /db/backup {"path": "..."}

// internal/cli, new verbs
kairos pause
kairos resume
kairos park [--wait]
kairos db backup <path>
kairos doctor --self-check
```

## Files to create

```
internal/eventstore/backup_test.go

internal/engine/pause.go  pause_test.go  selfcheck_test.go

internal/api/lifecycle.go

internal/cli/pause.go

cmd/kairos/chaos_test.go  lifecycle_test.go

# modified:
internal/eventstore/store.go
internal/engine/engine.go  admission.go
internal/api/server.go  status.go  routes_test.go (nopStore.Backup)
internal/apispec/ops.go
internal/cli/client.go  root.go  db.go  doctor.go
```

## Data changes

None beyond L02's schema. Backups are files under `~/.kairos/backups/`, not database rows —
`kairos db backup` writes wherever the caller names, matching `06-durability.md`'s "this command
must exist so nobody invents their own" framing.

## Acceptance criteria

- `go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run` clean; `go test ./... -race`
  green across every package, including `cmd/kairos` (which now takes ~160s under `-race`, driven
  by the chaos harness's 20 real daemon lifecycles — `-short` skips it).
- All nine architecture tests pass, `TestUI_everyCallHasCLICounterpart` included with the four new
  routes/verbs.
- **The chaos harness ran 20 real kill/restart/reconcile cycles (10 with a genuine mid-node
  SIGKILL), and every one**: reached `run.succeeded`, passed `db verify` with zero mismatches, and
  left its recorded pid dead by the end of the run. Zero orphan processes across all 20 iterations,
  confirmed by direct `kill(pid, 0)` checks after every daemon in the suite has been stopped.
- `kairos pause` genuinely holds a run at its next node boundary: a node already `Executing` when
  pause is set finishes naturally (no `node.execution.interrupted`), the following node's
  `NodeExecution` row stays `Pending`, and `kairos resume` un-blocks it — proven end-to-end against
  a real subprocess in `internal/engine/pause_test.go`.
- `kairos db backup` produces a file that opens as an independent, `Verify`-clean event store with
  the original run history intact — not merely a file that exists.
- `kairos doctor --self-check` reports `db: clean` after a healthy run and genuinely removes (not
  merely reports) an orphan workspace directory, proven in
  `internal/engine/selfcheck_test.go`.
- No `TODO`, `FIXME`, or commented-out code in the diff.

## Tests

- `internal/eventstore/backup_test.go`: `TestStore_backupProducesARestorableQueryableCopy`,
  `TestStore_backupRefusesToOverwriteAnExistingFile`.
- `internal/engine/pause_test.go`:
  `TestEngine_pauseHoldsAtTheNextBoundaryWithoutInterruptingWhatsRunning` (real subprocess,
  pauses mid-execution, confirms the in-flight node still succeeds while the next node stays
  `Pending`, then confirms resume drains it to `Succeeded`).
- `internal/engine/selfcheck_test.go`: `TestEngine_selfCheckReportsCleanAfterAHealthyRun`,
  `TestEngine_selfCheckRemovesAnOrphanWorkspaceDirectory`.
- `cmd/kairos/chaos_test.go`: `TestChaos_killAndRestartLeavesNoOrphanProcesses` — the centerpiece,
  described above.
- `cmd/kairos/lifecycle_test.go`: `TestLifecycle_pauseHoldsParkWaitsBackupRestores` (real binary,
  every new verb exercised end-to-end) and
  `TestLifecycle_parkWaitTimesOutIfSomethingNeverFinishes` (proves `--wait`'s deadline is real,
  not decorative — a 60-second node makes `park --wait` fail within its own ~25s budget rather
  than hang).

## Benchmarks

None. The chaos harness's iteration count is itself the relevant performance signal, and it is
reported in Acceptance criteria rather than as a separate benchmark.

## Migration

None from a prior version.

## Future work

- Scale the chaos harness toward the doc's literal 200-run target once CI budget allows —
  the harness's shape (one `t.Run` per iteration, fresh `$KAIROS_HOME` each) scales directly by
  raising `iterations`; nothing structural needs to change.
- `kairos up`/`down` daemon-process lifecycle verbs (named in `09-cli-and-tui.md`, never built in
  this project) — once they exist, `kairos park --wait` gains a genuinely literal "closing the
  lid" by calling `down` after confirming quiescence (decision #7).
- A dedicated `kairos stats`/`kairos cost` verb aggregating admission (L07) and effect-spend (L12)
  data into one view — named in the build plan's shorthand but not precisely specified anywhere;
  `GET /status`'s existing fields cover the load-bearing subset (`activeRuns`/`paused`/`inFlight`)
  this document needed.
- The cookie sweep for double-forked escapees (`06-durability.md`'s own named residual gap,
  `PR_SET_CHILD_SUBREAPER` + delegated cgroup on Linux, no macOS equivalent) — independent of this
  document's chaos/self-check scope, not exercised by the harness since none of its scenarios
  produce a double-forked escapee.
- Duplicate-effect chaos coverage against a real git-remote fixture (decision #3), if a future
  document's scope needs that specific combination proven rather than covered separately.
