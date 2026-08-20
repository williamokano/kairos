# L05 — Engine: advance, dispatch, replay, reconciliation

## Depends on

L04 (daemon/API/CLI), transitively L03/L02/L01. This is the first document to turn the `[]Cmd`
`domain.Advance` has always returned — computed and discarded since L01 — into real action.

## Scope

**In.**
- `internal/executor/local`: a minimal, real process executor — `Start`/`Signal`/`Wait`/`Cancel`,
  `Setpgid: true` process-group isolation, `proc.json` written *after* fork/exec and
  `spawning.json` *before* it, process identity as `(bootID, pgid, startedAt)` never bare PID, a
  `BootIDProvider` (`/proc/sys/kernel/random/boot_id` on Linux, `kern.boottime` sysctl on Darwin),
  and the TERM→wait(killGrace)→KILL cancellation sequence.
- `internal/executor/exectest`: a compliance suite any `local.Executor` must pass, plus an
  in-memory `Fake` for engine unit tests that never spawns a real process.
- `internal/engine`: `Engine`/`Config`/`New`/`Start`/`Stop`, `runShards[N]` (event routing by
  `hash(runID)`, in-order per run), the `Cmd` dispatch switch (`actor: rule` and `actor: shell`
  only — a fabricated placeholder actor and a minimal `/bin/sh -c` mapping, both scoped to this
  milestone), placeholder gate evaluation, and the reconciliation algorithm
  (`Reconcile`/`reconcileRun`/`recoverLost`).
- `internal/domain`: `advanceNodeExecutionLost` extended from a purely terminal transition to
  retry-or-route, mirroring `handleFailureOutcome`'s existing logic; four additive events
  (`EngineStarted`, `EngineStopped`, `EngineReconciled`, `ProcessOrphanReaped`) on a new
  `stream_id = "system"`, never folded by `Advance`.
- `internal/eventstore`: `SystemStream` constant; `RunStateProjection`/`RunIndexProjection`/
  `Store.Verify`/`Store.Rebuild` all skip it.
- `internal/registry`: `SideEffectFree bool` and resolved `RestartPolicy` (`rerun`/
  `fail-to-human`/`adopt`) on `NodeDef`; `adopt` rejected at publish time.
- `cmd/kairos/serve.go` rewritten: boot order becomes lock → store.Open → construct engine →
  `engine.Reconcile` (blocking) → `engine.Start` → bind/serve the API; SIGINT/SIGTERM trigger
  `engine.Stop` before exit.
- The five named tests: `TestExecutor_childInOwnProcessGroup`,
  `TestReconcile_rebootInvalidatesRecordedPGIDs`,
  `TestEngine_nonIdempotentNodeParksAfterRestart`, `TestEngine_survivesKillMidRun`,
  `TestEngine_ctrlCInterruptsThenResumes`.

**Out.** Workspace clone management (L06 — shell nodes get a bare scratch `cwd`, not a git
checkout); `llm`/`claude` actor invocation (L08); real gate evaluation (L10 — gates stay a
WARN-logged placeholder); effects/compensation/human decisions/TUI (L12/L13/L15); triggers beyond
the existing `POST /runs` (L16); a real persisted timer wheel; admission control.

## Documented decisions

1. **No `RunAdvanced` event type.** "Decision before action" is satisfied by transaction
   ordering: `RunStateProjection` folds `Advance`'s result transactionally inside the same
   `AppendIf` call that produced the triggering event, so by the time an engine shard goroutine
   can observe an event on the bus, the state it implies is already durable. Reconciliation
   re-derives anything undispatched by scanning `NodeExecution` rows whose status implies an owed
   action with no recorded outcome.
2. **Four additive system-stream events**, `stream_id = "system"`, reusing the `events` table —
   no new SQL table. `Advance` never folds them (not run-scoped, matching AGENTS §4 rule 6);
   `RunStateProjection`, `RunIndexProjection`, `Store.Verify`, and `Store.Rebuild` explicitly skip
   the stream rather than erroring on an unknown-event-type fold.
3. **`actor: "rule"` is a trivial, always-succeeds, zero-subprocess, in-process actor.** It
   appears nowhere in `03-workflows.md`'s actor enum — it exists only for this milestone's `n1`/
   `n4` nodes, registered as a named, minimal placeholder rather than a feature.
4. **`actor: "shell"` maps to `/bin/sh -c <prompt>`**, a minimal mapping for this milestone only —
   not `04-agents.md`'s real command-shape resolution for LLM CLIs (L08's job).
5. **Gate evaluation stays a placeholder**: empty `Gates` → `Passed: true`; non-empty → `Passed:
   true` plus a WARN naming the unimplemented gate (AGENTS §4 rule 1: never silently accept).
6. **No real timer wheel.** `CmdEnterWait` has a real, non-panicking dispatch arm that records
   wait bookkeeping but arms no `time.Timer` — nothing in this milestone's workflow exercises it;
   a persisted, suspend-safe timer wheel is Future work, not silently skipped.
7. **Shell nodes get a bare scratch `cwd`/`HOME`, not a workspace clone.** No
   `internal/workspace` package exists yet; that is L06's job.
8. **No admission control.** The milestone's four-node linear graph never runs concurrent node
   executions; adding a concurrency cap now would be scope AGENTS §7 forbids adding "because it
   looked easy."
9. **`SideEffectFree`/`RestartPolicy` live on `registry.NodeDef`, not `domain.Node`** — restart
   policy is a dispatch-time engine concern read off the `Definition` by `DefinitionRef`, matching
   `ProjectGraph`'s existing pattern. `RestartAdopt` parses but is rejected at validate time
   ("requires L06's reconciliation-loop machinery").
10. **Reconciliation runs to completion before the API starts serving.** `cmd/kairos/serve.go`'s
    boot sequence is strictly lock → `store.Open` → `engine.Reconcile` (blocking) →
    `engine.Start` → `api.Listen`. Readiness is enforced structurally — the socket does not exist
    until `engine.reconciled` has been recorded — not by a handler-level check.
11. **`domain.advanceNodeExecutionLost` retries before routing to failure**, bounded by the same
    `RetryPolicy.MaxAttempts` `handleFailureOutcome` already uses, instead of the engine
    synthesizing a fresh `NodeExecution` row externally (architecturally impossible: no legal
    event path from a terminal `Lost` exec to a new `Pending` row exists outside `Advance`'s own
    internal `dispatchExec`). This keeps `NodeExecutionLost` fully in-domain; the engine
    re-dispatches by feeding the event back through `Advance`, no bypass, no duplicated retry
    logic.
12. **`RestartPolicy` (rerun vs. fail-to-human) is engine-level, not domain-level**, so "parking" a
    fail-to-human node has no dedicated `ExecParked` domain transition. `domain.Advance` always
    computes the uniform retry-or-route decision; `recoverLost`, in the engine, checks the node's
    resolved `RestartPolicy` and simply does not dispatch the computed `CmdStartNode` when policy
    is fail-to-human — the fresh row stays permanently `Pending` and undispatched. This is the
    reachable, testable meaning of "parks after restart" for this document's scope.
13. **`Engine.Stop`'s final `EngineStopped` write uses its own fresh timeout, not the caller's
    shutdown ctx.** The caller's ctx bounds how long shutdown waits for graceful node
    interruption (`interruptExecuting`'s `Cancel` call selects on it while waiting out
    `killGrace`), and is routinely already expired by the time `wg.Wait` returns — a shutdown
    deadline shorter than `killGrace` is intentional (shutdown must not hang forever on a stuck
    process). Recording `EngineStopped` is the fact the next boot's unclean-exit detection depends
    on, so it must not inherit that exhausted budget.

## Public interfaces

```go
// internal/executor/local
type ExecSpec struct { RunID, NodeID, ExecID, Dir, WorkDir string; Env, Argv []string }
type Started struct { PID, PGID int; Nonce string; BootID string; StartedAt time.Time; Dir string }
type Signal int // SignalTerm, SignalKill
type Executor interface {
	Start(ctx context.Context, spec ExecSpec) (Started, error)
	Signal(ctx context.Context, pgid int, sig Signal) error
	Wait(ctx context.Context, pid int) (ExitResult, error)
	Cancel(ctx context.Context, pgid int, killGrace time.Duration) error
}
type BootIDProvider interface { BootID() (string, error) }
func DefaultBootIDProvider() BootIDProvider

// internal/executor/exectest
func RunComplianceSuite(t *testing.T, newExecutor func() *local.Local)
type Fake struct{ /* implements local.Executor in-memory */ }

// internal/engine
type Config struct {
	Store eventstore.Store; Executor local.Executor; BootID local.BootIDProvider
	WorkRoot string; KillGrace time.Duration; NumShards int; Logger *slog.Logger
}
func New(cfg Config) *Engine
func (e *Engine) Start(ctx context.Context) error
func (e *Engine) Stop(ctx context.Context) error
type ReconcileReport struct{ Recovered, Lost, OrphansReaped int }
func (e *Engine) Reconcile(ctx context.Context) (ReconcileReport, error)

// internal/domain, additive events
type EngineStarted struct{ BootID string }
type EngineStopped struct{}
type EngineReconciled struct{ Recovered, Lost, OrphansReaped int }
type ProcessOrphanReaped struct{ PGID int }

// internal/registry, additive NodeDef fields
SideEffectFree bool
RestartPolicy  RestartPolicy // RestartRerun | RestartFailToHuman | RestartAdopt
```

## Files to create

```
internal/executor/local/spec.go  bootid.go  bootid_linux.go  bootid_darwin.go  executor.go
internal/executor/local/spawn.go  signal.go  identity.go
internal/executor/local/executor_test.go  identity_test.go  signal_test.go

internal/executor/exectest/compliance.go  fake.go

internal/engine/doc.go  engine.go  shard.go  dispatch.go  actor_rule.go  actor_shell.go  gates.go
internal/engine/reconcile.go
internal/engine/reconcile_test.go  park_test.go  engine_test.go  stop_test.go

cmd/kairos/kill_mid_run_test.go  ctrl_c_test.go
cmd/kairos/testdata/milestone.yaml

# modified:
internal/domain/event.go  advance.go  advance_lifecycle_test.go
internal/events/schemas/*  fixtures/*  init.go  registry.go  fixtures_test.go  registry_test.go
internal/registry/definition.go  parse.go  defaults.go  validate.go
internal/registry/restartpolicy_test.go  (new)
internal/eventstore/store.go  projection_runstate.go  projection_runindex.go  rebuild.go
internal/eventstore/system_stream_test.go  (new)
internal/archtest/no_exec_outside_executor_test.go
cmd/kairos/serve.go
```

## Data changes

None beyond L02's schema — the system stream reuses the existing `events` table with
`stream_id = "system"`, excluded from `run_state_projection`/`run_index` folding by both
projections' `Apply` and by `Store.Verify`/`Store.Rebuild`.

## Acceptance criteria

- `go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run` clean; `go test ./... -race`
  green across every package.
- All nine architecture tests pass, including `TestArchitecture_noExecOutsideExecutor`'s
  three-entry exemption table (`internal/executor/local`, `internal/executor/exectest`,
  `cmd/kairos`) and `TestArchitecture_processesRecordedBeforeSpawn`.
- `TestEngine_survivesKillMidRun` (`cmd/kairos`) follows the milestone's exact ten-step procedure
  against the real built binary: publish → start a run → block until `node.execution.started{n2}`
  → SIGKILL the daemon → assert the orphaned child is still alive (the "mess exists" assertion,
  proving the test tests something) → restart → assert readiness only after `engine.reconciled` is
  read → assert by event (`process.orphan.reaped`, `node.execution.lost{n2,attempt:1}`,
  `node.execution.started{n2,attempt:2}`) → poll to `run.succeeded` → assert the run's event
  sequence is gapless `1..N` with no duplicates → assert on the world (the idempotency-guarded
  ledger has exactly one line, the killed attempt's directory is retained, the retried attempt's
  directory differs, the orphan is dead) → `kairos db verify` reports no mismatches.
- `TestEngine_ctrlCInterruptsThenResumes` proves the SIGINT path is genuinely different:
  `node.execution.interrupted{n2}` is recorded and the daemon exits 0 within the shutdown budget,
  with the child dead *before* any restart — no orphan reap on the next boot.
- `make cross` builds `CGO_ENABLED=0` for darwin/linux × arm64/amd64.
- No `TODO`, `FIXME`, or commented-out code in the diff.

## Tests

- `internal/executor/local`: the compliance suite (own process group, stdout/stderr captured to
  files, `proc.json`/`spawning.json` ordering), boot-ID comparison across a simulated reboot, the
  TERM-then-KILL cancellation sequence against a process that ignores SIGTERM.
- `internal/domain/advance_lifecycle_test.go`: `TestAdvance_killMidNodeThenRestartRecordsLostThenRetries`
  (retry dispatch, `PriorExecID` chain, confirmed attempt-2 start) and
  `TestAdvance_lostRoutesToFailOnceAttemptsAreExhausted`.
- `internal/eventstore/system_stream_test.go`: `TestStore_verifyAndRebuildSkipTheSystemStream`.
- `internal/registry/restartpolicy_test.go`: default resolution from `SideEffectFree`, explicit
  override, `adopt` rejected at publish.
- `internal/engine/reconcile_test.go`: `TestReconcile_rebootInvalidatesRecordedPGIDs` (injectable
  boot-ID spy — a stale bootID is never signalled), `TestReconcile_orphanedAliveProcessIsKilledAndMarkedLost`.
- `internal/engine/park_test.go`: `TestEngine_nonIdempotentNodeParksAfterRestart`.
- `internal/engine/engine_test.go`: `TestEngine_liveLoopDrivesARuleThenShellWorkflowToSuccess`, a
  full real end-to-end run with no crash.
- `internal/engine/stop_test.go`: `TestEngine_stopInterruptsExecutingNodesBeforeKilling`, a real
  subprocess, asserting the process is dead and the interrupted event recorded.
- `cmd/kairos`: the two named flagship tests described in Acceptance criteria.

## Benchmarks

None. Nothing introduced here is on L02's durability-sensitive hot path at a scale that warrants
one yet.

## Migration

None from a prior version.

## Future work

- L06 extends `internal/executor/local` (real workspace clones, `adopt` restart policy, reaping
  polish) rather than recreating it — this document's package is deliberately minimal, authorized
  by the milestone's own requirement for real subprocess evidence, not built ahead of scope.
- A real, persisted, suspend-safe timer wheel for `CmdEnterWait`/`wait:` nodes (decision #6).
- Admission control once concurrent node executions within a run are possible (decision #8).
- ADR 0012's PID-file lock is revisited once `internal/executor/local` is fuller (it already
  exists as of this document, ahead of the ADR's original L06 trigger — worth re-reading ADR 0012
  now that the precondition is closer).
- Real gate evaluation (L10) replaces decision #5's WARN-logged placeholder.
