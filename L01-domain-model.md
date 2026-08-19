# L01 — Domain Model

## Depends on

L00 (bootstrap). `internal/domain` existed as an empty package with a `doc.go`, exercised only
by `TestArchitecture_domainPurity`'s real-tree check against an empty tree; this document is
the first to give that check something real to check.

## Scope

**In.**
- `internal/domain`'s full pure type system: `Event`, `Cmd`, `Graph`/`Node`/`Edge` (a minimal,
  YAML-agnostic, fully-resolved workflow shape — no parsing, no JSONPath, no defaulting),
  `RunState`/`RunStatus`, `NodeExecution`/`ExecStatus`, `Finding`, `WaitSpec`, `RetryPolicy`,
  `LoopGuard`.
- The `Run` and `NodeExecution` state machines: closed enums, legal-transition tables as data
  (`transitions.go`), and the invariants each transition upholds.
- `Advance(state RunState, ev Event, now time.Time) (RunState, []Cmd, error)` — the single pure
  decision function: dispatch (`Pending → Executing` via a round-tripped
  `NodeExecutionStarted` confirmation, vs `Pending → Waiting` entered directly, since a wait has
  no spawn that can fail — 06-durability.md: "a wait's entire footprint is three rows"), the
  schema-valid → gates → edges pipeline order, retry-vs-exhausted routing (`Attempt` against
  `RetryPolicy.MaxAttempts`), rejected-loop-vs-escalate routing (`Iteration` against
  `LoopGuard.MaxIterationsPerNode`), terminal-outcome-to-sink routing (`$succeed`/`$fail` via
  `Graph.Edges`), and the Run-level conclusions (`Succeeded`/`Failed`) derived from those routes
  within the same call — no separate consumed event for them (see "Documented decisions"
  below).
- Deterministic `NodeExecution` IDs: `execID(nodeID, attempt, iteration)` — a pure string
  derivation, not a minted ID, so `Advance` can name a row it is asking the engine to create
  without generating anything (AGENTS §4 rule 4: no randomness inside domain).
- Table-driven unit tests covering every legal and many illegal transitions, including two
  full cross-product tests (`transitions_test.go`) iterating every `RunStatus`/`ExecStatus`
  against every event type, under `-race`, per AGENTS §5.

**Out.**
- Event persistence, the schema registry, fixtures, and SQLite projections — L02. `Advance` is
  written and tested with zero store, per the build-order rule "L01 before L02: the domain
  must be written with no store to depend on."
- The engine's advance loop, dispatch to services, replay, and reconcile-on-startup — L05. L01
  has no consumer; `Advance` is called only from its own tests.
- YAML parsing, JSONPath resolution, input/output schema inference, document-order edge
  defaulting — L03. `internal/domain/expr` and `internal/domain/schema` are not created here.
- Gate *evaluation* mechanics (per-gate kind, findings adapters, severity, waivers, quorum) —
  L10. L01 only consumes the aggregate `NodeGatesEvaluated{Passed, Findings}` fact.
- Effects, policy, `denied` outcome semantics — L11/L12. `FailReason` deliberately has no
  `Denied` value yet (see Future work).
- The human-decision aggregate and typed decision object, effect confirmation — L13. L01 only
  has `HumanTaskCreated`/`HumanTaskAnswered` as generic facts with an opaque
  `json.RawMessage` output.
- Conversations — L14.
- Trigger *sources* — L16. `TriggerReceived.TriggerRef` is opaque.
- Spawn/join dispatch, child-run lineage, `maxSpawnDepth`, wave scheduling — L17. Only the
  `Degraded` run state and a `RunDegraded`/`RunDegradedResolved` event pair exist, as the
  03-workflows.md-mandated placeholder.
- Fork/replay mechanics (`run.forked`, prefix copy) — L18.
- `internal/eventstore`'s `Projection` interface itself — L02 defines it and may call
  `domain.Advance` under the hood; L01 does not know `internal/eventstore` exists.

## Documented decisions

Design points the source docs leave implicit, decided here rather than guessed silently:

1. **`Advance` is the projection function itself.** `state` after N events is `Advance`
   applied N times from `RunState{}` — this is what "state is a projection" (L2) means
   operationally, and what L02's `Projection.Apply` will call under the hood.
2. **Two independent counters** on `NodeExecution`: `Attempt` (bounded by
   `RetryPolicy.MaxAttempts`, driven by `failure`/`timeout`/`schema-invalid`) and `Iteration`
   (bounded by `LoopGuard.MaxIterationsPerNode`, driven by `rejected`) — 03-workflows.md's own
   `implement` node example has both a `retry` block and gates, so these must be separable.
3. **A terminal `NodeExecution` row is never mutated again.** "Retry" always allocates a new
   row with `PriorExecID` set, chaining lineage. `ExecAdopted` is a legal enum value from L01
   even though only reachable starting L06 — widening the enum later is the "unrecoverable
   later" mistake AGENTS.md warns about.
4. **`onTimeout: park` causes no status transition** — only `Overdue = true` on the
   still-`Waiting` execution, per 03-workflows.md's literal wording ("it never proceeds and
   never fails, it just waits and shows a badge"). Only `onTimeout: escalate` moves to
   `Parked`.
5. **`Parked` consolidates three doc-cited triggers** (non-idempotent-node-after-crash,
   wait-timeout-escalate, loopguard-exceeded-escalate) into one status with a `ParkReason` —
   all three mean "stop automatic progress, surface a human task."
6. **`RunSucceeded`/`RunFailed` are not separate consumed `Event` types.** They are derived,
   within the same `Advance` call that processes a node's terminal outcome, by following
   `Graph.Edges` to a sink. Recording a durable marker event for observers (SSE, etc.) is the
   engine's job (L05), not something domain needs to fold back in.
7. **`Waiting` is entered directly**, without a round-tripped confirmation event, because
   entering it has no failure mode to confirm (unlike spawning a process, which
   `NodeExecutionStarted` exists to confirm actually happened).

## Public interfaces

```go
func Advance(state RunState, ev Event, now time.Time) (RunState, []Cmd, error)

type Event interface { EventType() string; isEvent() }
type Cmd interface { isCmd() }

// Fifteen concrete Events: TriggerReceived, RunStarted, RunRejected, RunCancelled,
// RunDegraded, RunDegradedResolved, NodeExecutionStarted, NodeOutputReceived,
// NodeWaitResolved, NodeGatesEvaluated, NodeExecutionFailed, NodeExecutionInterrupted,
// NodeExecutionLost, NodeExecutionAdopted, HumanTaskCreated, HumanTaskAnswered.

// Six concrete Cmds: CmdStartNode, CmdEvaluateGates, CmdEnterWait, CmdCreateHumanTask,
// CmdArmTimer, CmdSignalNode.

type Graph struct { Entry NodeID; Nodes []Node; Edges map[NodeID]map[EdgeTrigger]NodeID }
type Node struct { ID NodeID; Wait *WaitSpec; Retry RetryPolicy; LoopGuard LoopGuard }

type RunStatus string // pending running degraded succeeded failed cancelled rejected
type RunState struct {
    ID         string
    Status     RunStatus
    Graph      Graph
    Executions map[NodeID][]NodeExecution
}
func (s RunState) Terminal() bool
func (s RunStatus) Terminal() bool
func (s RunStatus) Valid() bool

type ExecStatus string // pending executing waiting adopted succeeded failed rejected interrupted lost parked
type NodeExecution struct {
    ExecID, PriorExecID string
    NodeID               NodeID
    Status               ExecStatus
    Attempt, Iteration   int
    Overdue              bool
    Reason               FailReason
    ParkReason           ParkReason
    Findings             []Finding
}
func (s ExecStatus) Terminal() bool
func (s ExecStatus) Valid() bool
```

## Files to create

```
internal/domain/event.go                  # Event interface + 15 concrete events
internal/domain/cmd.go                    # Cmd interface + 6 concrete cmds
internal/domain/graph.go                  # Graph, Node, Edge types, WaitSpec, RetryPolicy, LoopGuard
internal/domain/run.go                    # RunState, RunStatus, Terminal(), the run-side fold helpers
internal/domain/nodeexecution.go          # NodeExecution, ExecStatus, FailReason, ParkReason, Finding
internal/domain/transitions.go            # legal-transition tables (data) for RunStatus and ExecStatus
internal/domain/advance.go                # Advance() and its per-event-type handlers
internal/domain/errors.go                 # ErrIllegalTransition, ErrRejectedNeedsFindings, etc.
internal/domain/run_test.go
internal/domain/nodeexecution_test.go
internal/domain/transitions_test.go       # the two full cross-product tests
internal/domain/advance_test.go           # one scenario per event type
internal/domain/advance_lifecycle_test.go # full hand-built run-shaped sequences
```

## Data changes

None. `internal/domain` has zero I/O and touches no filesystem or database (AGENTS §2). The
`events` table these types will eventually populate is L02's.

## Acceptance criteria

- `go build ./...`, `go vet ./...`, `golangci-lint run` clean; `go test ./... -race` green.
- `TestArchitecture_domainPurity`'s real-tree subtest now exercises non-trivial code: verified
  passing against the populated `internal/domain` package.
- `TestAdvance_runLevelEventsRespectTheLegalTransitionTable` and
  `TestAdvance_nodeLevelEventsRespectTheLegalTransitionTable` (`transitions_test.go`) assert,
  for every `RunStatus`/`ExecStatus` × event-type pair, that `Advance` returns
  `ErrIllegalTransition` exactly when the pair is absent from the legal-transition table —
  verified.
- `NodeGatesEvaluated{Passed:false, Findings: nil}` returns `ErrRejectedNeedsFindings` —
  `TestAdvance_gatesRejectedWithNoFindingsIsAnError`.
- `CmdEnterWait` is never returned without `CmdArmTimer` when `WaitSpec.TimeoutAt` is set —
  `TestAdvance_waitSpecWithTimeoutAlwaysArmsATimer`.
- `NodeWaitResolved{Outcome: TimedOut}` with `onTimeout: park` produces no status transition —
  `TestAdvance_ciWatchPollTimeoutWithParkLeavesNodeWaiting` asserts `Status` unchanged and
  `Overdue == true`.
- Loop-guard and retry boundaries are off-by-one tested —
  `TestAdvance_loopGuardExceededParksAndAsksAHuman` and
  `TestAdvance_retryBoundaryOffByOneOnAttempt` both exercise the N-1-loops / N-escalates
  boundary.
- `Advance` never generates an ID or reads a wall clock — enforced by
  `TestArchitecture_domainPurity`'s `time.Now` scan plus
  `TestAdvance_advanceIsDeterministicForIdenticalInputs`, which runs `Advance` twice with
  identical `(state, ev, now)` and asserts identical results via `reflect.DeepEqual`.
- No `TODO`, `FIXME`, or commented-out code in the diff.

All verified locally: `go build ./...`, `go vet ./...`, `go test ./... -race`,
`golangci-lint run`, and `make cross` all pass; `go test ./internal/domain/... -v` shows all
24 tests passing.

## Tests

- `run_test.go`, `nodeexecution_test.go`: enum validity and `Terminal()` exhaustiveness,
  independent of `Advance`.
- `transitions_test.go`: the two full cross-product legal-transition tests described above.
- `advance_test.go`: one scenario per event type against its legal prior state(s) — dispatch,
  schema-invalid short-circuit, gate-evaluation request, success routing, rejection requiring
  findings, the rejected self-loop bounded by `LoopGuard`, the retry boundary bounded by
  `RetryPolicy`, and the determinism check.
- `advance_lifecycle_test.go`: full hand-built sequences with no store or engine — this is
  L01's whole verification story, since there is no consumer yet: human-approval
  wait-then-resume, CI-watch poll-timeout-with-park (stays `Waiting`), poll-timeout-with-escalate
  (→ `Parked`), and kill-mid-node → `Lost` (terminal, and confirms L01 does not itself
  fabricate a retry for a lost node — that dispatch decision belongs to L05/L06).
- All under `go test ./internal/domain/... -race`, per AGENTS §5.

## Benchmarks

None. `Advance` is a pure in-memory fold with no I/O; nothing here is on a durability-sensitive
hot path yet (that's `BenchmarkAppendIf_singleEvent`, L02's).

## Migration

None. No schema exists yet for these types to migrate; L02 defines the event store schema and
its registry entries for the concrete `Event` types this document introduces.

## Future work

- L02 (event store) gives every `Event` type here an `event_type` string (already defined via
  `EventType()`), a JSON Schema, and a version-1 fixture, and wires
  `internal/eventstore`'s `Projection.Apply` to call `domain.Advance` under the hood.
- L03 (definition + validator) is what actually produces a `Graph` value from YAML:
  document-order edge defaulting, `rejected → self`, `failure/timeout/denied → $fail`,
  input-schema inference, and the "`onTimeout` is a required field" publish-time check (L01
  only enforces domain's own half: never returning an unarmed wait).
- L05 (engine) writes the advance loop that calls `Advance` in sequence per run stream,
  appends `RunAdvanced{cmds}` before dispatch, records `RunSucceeded`/`RunFailed` observer
  events from the derived state change, and decides when to dispatch a fresh
  `NodeExecutionStarted` for a `Lost`/`Interrupted` node (`restartPolicy: rerun`) — proven by
  `TestReconcile_rebootInvalidatesRecordedPGIDs` (12-build-plan.md), not by anything here.
- L06 (local executor) implements `restartPolicy: adopt`; L05 only implements `rerun`, per
  12-build-plan.md's explicit staging.
- L10 (constraints + gates) replaces the aggregate `NodeGatesEvaluated{Passed, Findings}`
  fact's *producer* with real per-gate evaluation, severity, and waivers; the `Finding` type
  stays deliberately minimal here and may grow fields there.
- L11/L12 (policy, effects) add `FailReason.Denied` and the `denied` edge trigger's real
  semantics — deliberately absent from `FailReason` here since nothing in scope produces it.
- L13 (human decisions) replaces the opaque `HumanTaskAnswered.Output json.RawMessage` with
  the typed decision object and the effect-confirmation ladder; the `Waiting`/`Parked` state
  machine itself does not change shape.
- L17 (child runs) computes the actual condition behind `RunDegraded` (a child's join outcome)
  and adds spawn/join dispatch; L01's placeholder only lets the state be entered and exited by
  an already-computed fact.
