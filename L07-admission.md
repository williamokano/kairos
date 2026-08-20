# L07 — Admission: pools, budgets, and the one-writer-per-workspace lock

## Depends on

L05 (engine) only — its single inbound edge in `12-build-plan.md`'s flowchart. Transitively L04/L03/
L02/L01/L00. Built after L06 and L08 in this session's actual order (both were unblocked sooner and
this document sat on the graph unbuilt in the meantime); nothing here depends on either.

## Scope

**In.**
- `internal/admission`: answers one question per node execution — "may it start right now?" —
  returning `Granted(claims)`, `Queued(position)`, or `Denied(reason)`. Implements 02-config.md's
  ordered admission rules 1 (draining), 2 (node concurrency), 3 (one-writer-per-workspace), 4 (model
  slots), 5 (daily spend cap), and 7 (reject past `maxQueued`) — see Documented decisions for rules
  6, 8, 9.
- All-or-nothing claims, in canonical pool order, leased and released explicitly. Human-readable
  denial strings matching 02-config.md's examples verbatim (`"4 of 4 slots busy"`, `"$24.10 of $25.00
  spent today"`).
- `internal/config`: `AdmissionNodeSlots`/`AdmissionMaxQueued`/`DailyUSD`, env-var-sourced (matching
  L06/L08's `WorkspaceRepo`/`LLMBinary` convention — no `config.yaml` parser exists yet), defaulted per
  02-config.md's defaults table.
- `internal/engine` wiring: `dispatchStartNode` gates every `CmdStartNode` through admission before
  any actor ever spawns a process. `Queued` holds the request in a FIFO retry queue with no side
  effect recorded (admission runs strictly before `NodeExecutionStarted`). `Denied` records a
  zero-duration started-then-failed attempt (see decision #4). A release — from `dispatchRuleActor`'s
  synchronous completion or `reapShell`/`reapLLM`'s process exit — drains the queue in order.
  `Engine.Stop` sets `draining` before cancelling, so every still-queued request is denied
  ("shutting down") during shutdown rather than silently dropped.

**Out** (see Documented decisions for why each is deferred, not silently guessed at):
- Rule 6 (`maxOpenDecisions`) — no human queue exists yet (L13).
- Rule 8 (runner-label matching) — no runner but `local` exists yet (a later, differently-numbered
  phase); would always trivially pass.
- Rule 9 (domain-lane exhaustion) — `13-domains.md` frames per-domain lanes/budgets as its own later
  extension to the pool model, not this document's job.
- `cpu.heavy` pool — no per-node CPU-class field exists in `registry.NodeDef`.
- Placement/scheduling of any kind (permanently foreclosed by ADR 0004, not deferred).
- Daily-spend-window reset (a day boundary needs a durable, timezone-aware clock source this
  document doesn't have reason to build yet).
- Remote runners (a much later phase; unrelated to `07-runners.md`'s own numbering, which this
  document's "L07" name coincidentally shares but is not the same document).

## Documented decisions

1. **No `config.yaml` parser exists yet.** `internal/config` still only resolves individual env vars
   (`KAIROS_ADMISSION_NODES`, `KAIROS_ADMISSION_MAX_QUEUED`, `KAIROS_DAILY_USD`), exactly matching
   L06/L08's established minimal pattern (`WorkspaceRepo`/`LLMBinary`). Full YAML parsing for
   `admission:`/`models:`/`limits:` blocks — including `admission.pools.cpu.heavy` and
   `models.<class>.slots` as a map — is deferred to whichever document adds the general config-file
   loader; `engine.Config.Admission.ModelSlots` accepts a `map[string]int` today, but nothing wires
   it from `config.yaml` yet.
2. **The workspace write-lock (rule 3) is keyed by the daemon-wide `WorkspaceRepo` string**, not a
   per-project registry key. L06/L08 already scoped the engine to one daemon-wide repo (their own
   Future work item); this document's write-lock claim reuses that same single key rather than
   inventing per-project admission ahead of the document that adds multi-project support.
3. **Rule 5 (`dailyUSD`) enforces against a node's declared `resources.model.maxCostUSD`, never
   metered actual spend.** NL-30 already registers that real LLM cost is unknown (no stream parsing,
   L08). This document adds a real, testable enforcing code path on top of that limitation rather
   than waiting for true metering — see NL-30's L07 update in `11-limitations.md`. A node with no
   declared cost estimate contributes 0 to the daily total, same as before this document existed.
4. **A `Denied` node execution is recorded as a zero-duration started-then-failed attempt, not a
   direct Pending→Failed transition.** `internal/domain`'s `legalExecEvents` table only accepts
   `NodeExecutionFailed` against an `Executing` exec; `ExecPending` accepts only
   `NodeExecutionStarted`. Discovered by `TestEngine_admissionDeniesPastCapacityWithReason` hitting
   `ErrIllegalTransition` directly. Fixed at the two call sites this document introduces
   (`dispatchStartNode`, `drainPending`) via a shared `denyNode` helper that appends
   `NodeExecutionStarted` immediately before `NodeExecutionFailed`. The identical trap exists,
   unfixed, in `dispatchShellActor`'s/`dispatchLLMActor`'s own pre-existing early-failure returns
   (L06/L08) — registered as NL-31 rather than silently fixed in files this document doesn't own
   (AGENTS §7: "do not refactor code another document owns").
5. **`drainPending` is fully serialized by a dedicated `drainMu`, not just `pendingMu`.** Two
   concurrent releases (two different node executions finishing at nearly the same moment) can each
   observe the same queue head before either pops it — `pendingMu` alone protects individual
   read/pop/append operations, not the read-decide-pop sequence as a whole. Found via repeated
   `-race`/`-count=5` runs of the admission engine tests before the fix; `drainMu` makes the whole
   drain-one-item-at-a-time loop atomic with respect to itself.
6. **`Engine.Stop` sets `admit.SetDraining(true)` before cancelling the run context**, so any node
   still sitting in the pending queue at shutdown is denied with `"shutting down"` rather than
   silently forgotten. The subsequent record attempt commonly fails with "context canceled" (the run
   context is already cancelled by then) and is logged at ERROR rather than swallowed — correct,
   not a bug: nothing was ever granted to that queued request, so there is no state to lose, only a
   diagnostic write that couldn't land.
7. **Manager's zero-value `Config` fields mean "unlimited," not "always deny."** `NodeSlots: 0`,
   `DailyUSD: 0`, `ModelSlots[class]: 0` all disable that rule's capacity check entirely. Real
   defaults are resolved by the caller (`internal/config.Load`, matching 02-config.md's defaults
   table: `nodes: min(4, NumCPU/2)`, `maxQueued: 40`, `dailyUSD: 25`) — a bare `admission.Config{}`
   passed directly to `admission.New` (as every pre-L07 engine test still does) is a deliberate
   "no limit," not an accidental full lockout.
8. **`resolveNodeActor` is factored out of `dispatchStartNode` into `admission.go`** so
   `drainPending` can re-run the exact same node/actor/retry-mutate resolution for a previously
   queued `CmdStartNode` without duplicating that logic — the alternative (storing the resolved
   `registry.NodeDef`/actor string alongside the queued command) would let a stale resolution survive
   a definition reload; re-resolving on every drain attempt is deliberately cheap and always current.

## Public interfaces

```go
// internal/admission
type Outcome int // Granted | Queued | Denied
type Claims struct{ /* opaque */ }
type Decision struct {
	Outcome  Outcome
	Claims   Claims
	Reason   string
	Position int
}
type Request struct {
	RunID, NodeID    string
	NodeSlot         bool
	WorkspaceKey     string
	ModelClass       string
	EstimatedCostUSD float64
	QueueDepth       int
}
type Config struct {
	NodeSlots  int
	ModelSlots map[string]int
	DailyUSD   float64
	MaxQueued  int
}
func New(cfg Config) *Manager
func (m *Manager) SetDraining(draining bool)
func (m *Manager) TryAdmit(req Request) Decision
func (m *Manager) Release(claims Claims)

// internal/config, additive Config fields
AdmissionNodeSlots int
AdmissionMaxQueued int
DailyUSD           float64

// internal/engine, additive Config field
Admission admission.Config
```

## Files to create

```
internal/admission/admission.go  admission_test.go

internal/engine/admission.go  admission_test.go

# modified:
internal/engine/engine.go       (admit *admission.Manager, pending queue, claims map, drainMu, Stop draining)
internal/engine/dispatch.go     (admission gate in dispatchStartNode, runActorDispatch split out, denyNode)
internal/engine/actor_shell.go  (releaseAndDrain on every exit path)
internal/engine/actor_llm.go    (releaseAndDrain on every exit path)
internal/config/config.go       (AdmissionNodeSlots/AdmissionMaxQueued/DailyUSD, defaultNodeSlots)
cmd/kairos/serve.go             (wires engine.Config.Admission from cfg)
11-limitations.md               (NL-30 updated, NL-31 added)
```

## Data changes

None. Admission state is entirely in-memory (`internal/admission.Manager`) and does not survive a
restart — every pool starts empty on daemon boot, which is correct: a restart means no process is
actually holding any claim from before (L05/L06's reconciliation already re-derives real process
state from the log and `/proc`, independent of admission's in-memory bookkeeping).

## Acceptance criteria

- `go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run` clean; `go test ./... -race`
  green across every package, including `-count=5` repeats of the admission engine tests (the
  `drainMu` race fix, decision #5, has no flake margin left to hide behind).
- All nine architecture tests pass; `make cross` builds all four platform/arch combinations.
- All-or-nothing claim semantics proven under both single-goroutine (`TestTryAdmit_allOrNothing…`)
  and concurrent (`TestTryAdmit_workspaceWriteLockIsExclusiveUnderConcurrency`, 50 goroutines) load:
  never more than one grant for an exclusive resource, never a leaked partial claim.
- A `Denied` node execution produces a real `NodeExecutionFailed` with the denial reason verbatim in
  `Message`, not a silent drop or a generic message
  (`TestEngine_admissionDeniesPastCapacityWithReason`).
- A `Queued` node execution genuinely runs once a slot frees, driven against the real engine with two
  competing runs, not just `internal/admission` in isolation
  (`TestEngine_admissionQueuesThenRunsOnceASlotFrees`).
- No `TODO`, `FIXME`, or commented-out code in the diff.

## Tests

- `internal/admission/admission_test.go`: draining denies everything; node-slot exhaustion queues
  under `maxQueued` and denies past it; model-slot exhaustion; daily budget cap; all-or-nothing
  claims leak no partial grant; workspace write-lock exclusivity under 50 concurrent goroutines;
  workspace lock releases and re-admits sequentially.
- `internal/engine/admission_test.go`: denial produces a `NodeExecutionFailed` with the verbatim
  reason (three-run scenario exercising both `Queued` and rule 7's `Denied`); a queued node
  genuinely runs once the slot-holder finishes.
- Every pre-existing L05/L06/L08 test still passes unmodified against a zero-value
  `admission.Config{}` (decision #7), proving admission is additive, not a breaking change to
  already-shipped behavior.

## Benchmarks

None. `TryAdmit`/`Release` are in-memory map operations under one mutex — not on L02's
durability-sensitive hot path, and not yet exercised at a scale (thousands of concurrent claims)
where their cost would be interesting to measure.

## Migration

None from a prior version.

## Future work

- Full `config.yaml` parsing for `admission:`/`models:`/`limits:` (decision #1) — the general
  config-file loader this document deliberately did not build.
- Rules 6, 8, 9 (decision list above) once their prerequisites exist: a human queue (L13), a second
  `Runner` implementation (a later phase), and domain profiles (`13-domains.md`'s own extension).
- Daily-spend-window reset — a real day boundary, timezone-aware, surviving a restart (today's
  `dailySpent` counter is process-lifetime-only and resets to zero on every daemon restart, which is
  strictly more permissive than the real 24-hour window 02-config.md describes).
- NL-31: route `dispatchShellActor`'s and `dispatchLLMActor`'s pre-existing early-failure returns
  through a `denyNode`-shaped helper instead of `appendNodeFailed` directly, closing the same
  illegal-transition trap this document fixed only at its own two call sites.
- `cpu.heavy` pool: needs a per-node CPU-class declaration in `registry.NodeDef` that does not exist
  yet — no document has proposed adding one.
- Reconciling a node's `resources.model.maxCostUSD` *estimate* (decision #3) against real observed
  spend, once NL-28/NL-30's stream-parsing gap closes.
