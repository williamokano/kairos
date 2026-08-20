# L10 — Constraints: gates that cannot be skipped

## Depends on

L08 (actor SDK + sessions) only — its single inbound edge in `12-build-plan.md`'s flowchart.
Transitively L06/L05/L04/L03/L02/L01/L00. `internal/admission` (L07) is consumed but not a graph
dependency of L10 — the `cpu.heavy` permit reuses L07's existing pool mechanism rather than
requiring L07 to have shipped first; it happened to already exist by the time this document was
built.

## Scope

**In.** The phase-0 slice the build plan calls "constraints slice 1: structural + deterministic,
findings, one gate" — real evaluation for two of `05-gates.md`'s ten gate kinds, replacing L05's
WARN-logged placeholder (`internal/engine/gates.go`'s `evaluateGates`).

- `internal/registry`: a `GateDef`/`GateKind` model and a top-level `gates:` map on `Definition`,
  parsed from the workflow YAML's own raw document (not the constitution.yaml `05-gates.md`
  describes — see decision #1). Nodes keep referencing gates by name via their existing
  `Gates []string`.
- `internal/constraint` (new package, matching the name AGENTS.md's layout table already
  reserves): `Evaluator.Evaluate` for `expr` (in-process, `expr-lang/expr`, ADR 0013) and
  `command` (a real subprocess through `internal/executor/local`, a `cpu.heavy` admission permit,
  preflight binary resolution, `Setpgid`, capped ring-buffer evidence, exit-code comparison, a
  `golangci-json` findings adapter).
- `internal/domain`: `ConstraintEvaluated`, a new additive, run-scoped, no-op-folded event —
  one per gate evaluation, pass or fail.
- `internal/engine/gates.go`: real `evaluateGates` — reads the node's already-recorded output back
  from the event log, resolves each declared gate ID, evaluates in declared order (`strategy: all`,
  `05-gates.md`'s local default), records `constraint.evaluated` per gate, and appends the
  aggregate `NodeGatesEvaluated{Passed, Findings}` — the event `internal/domain`'s existing
  `advanceNodeGatesEvaluated` (L01) already knows how to fold into a loop-back-with-findings or a
  park, bounded by the workflow's existing `limits.loopGuard`.
- `adr/0013-expr-lang-for-expr-gates.md`: the expression-library choice.

**Out** (named here, not built — see Documented decisions for why each is deferred):
- `file`, `regex`, `git-diff`, `coverage`, `judged` gate kinds — `05-gates.md`'s fuller "constraints
  slice 2," a separate, later, 7-day phase-1 line item.
- The three non-code domain gate kinds (`grounded`, `recipients`, `outbound-scan`) — `13-domains.md`,
  phase-1 domain layer.
- The constitution resolution/merge system (`kairos/baseline` + project + repo tiers), policy
  (`~/.kairos/policy.yaml`), waiver grant/approval, quorum judging, effect confirmation — all L11
  and later.
- A dedicated `cpu.heavy` admission pool type — reuses L07's existing generic `ModelClass`-keyed
  pool mechanism instead (decision #4); L07's own Future work flagged this gap and named no owner.
- Full artifact-store integration for gate evidence (`05-gates.md`: "64 KiB tail as evidence, full
  output as an artifact") — only the 64 KiB tail is implemented; the full-output artifact half is
  Future work.
- `strategy: fail-fast` and any constitution-level override of `strategy: all` — L11 (policy).

## Documented decisions

1. **Gate definitions live in the workflow YAML's own top-level `gates:` map, not
   `05-gates.md`'s three-tier constitution.yaml resolution.** That real design — `kairos/baseline`
   (compiled in) merged with a project constitution outside every workspace and a repo-level,
   hash-pinned file — needs a merge engine, a content-hashing/pinning mechanism, and a project
   registry concept none of which exist yet; it is L11's job. This document scopes to "a workflow
   author can declare a gate inline in the same file as the nodes that use it," which is enough to
   prove real evaluation end-to-end without building infrastructure a later document owns.
2. **A node's `Gates []string` entry with no matching local `gates:` definition is not a publish
   error.** `03-workflows.md`'s own canonical `fix-issue.yaml` example declares
   `gates: [build, lint, no-todos, no-secrets, guardrails-untouched]` with no top-level `gates:`
   block anywhere — those names are meant to resolve against the real constitution/library L11
   builds. Rejecting them at publish time today would break that canonical example and every test
   built on it. Instead, `internal/engine`'s `evaluateGates` WARN-logs and skips an unresolved gate
   ID by name (AGENTS §4 rule 1: never silently accept — a real, honest, visible gap, exactly L05's
   original placeholder posture for the same underlying uncertainty). `registry.Validate` still
   rejects a *locally defined* gate's structural mistakes (an unsupported `kind`, an empty
   `command`, an absolute `workdir`) — those are real errors, independent of the resolution
   question.
3. **`expr-lang/expr` (ADR 0013) is a new approved dependency**, since AGENTS.md's approved table
   predates any gate-evaluation document and names no expression engine. Pure Go, no cgo, compiles
   once and reuses the program per evaluation, and its built-in `all`/`any`/`filter`/`map`
   functions cover `05-gates.md`'s own example expressions' shape — at the cost of a syntax
   divergence from the doc's literal `$.output.foo[*]` JSONPath examples (ADR 0013's Consequences
   section states this plainly rather than pretending the match is exact).
4. **The `command` gate's `cpu.heavy` permit reuses `internal/admission`'s existing
   `ModelClass`-keyed pool** (`admission.Request{ModelClass: "cpu.heavy"}`) instead of adding a
   dedicated pool kind to `internal/admission.Config`. `ModelClass` has never actually required its
   key to name an LLM model class — it indexes a `map[string]int` by an arbitrary string — so this
   is a real, capacity-enforced pool, not a naming fiction. L07-admission.md's own Future work
   section named this exact gap ("`cpu.heavy` pool: needs a per-node CPU-class declaration... no
   document has proposed adding one") with no owner; this document claims it. A denied or queued
   `cpu.heavy` permit fails the gate immediately with the permit's own denial reason rather than
   the evaluator blocking or the engine growing a second queue — `internal/admission`'s queue
   machinery is scoped to `CmdStartNode` admission (L07), not gate-level admission, and extending
   it is Future work if `cpu.heavy` contention becomes real under load.
5. **`local.Executor.Wait` does not respect context cancellation** (it blocks on the real `wait4`
   syscall via `cmd.Wait()`, which has no context-aware variant) — discovered while implementing
   the `command` gate's `Timeout` field: an initial `context.WithTimeout`-wrapped `Start`/`Wait`
   call silently did nothing, since `Wait` never even looks at the context it's handed. Fixed with
   `waitWithTimeout`, which races a timer against `Wait` in a goroutine and, on timeout, calls the
   existing `Executor.Cancel` (the same TERM→grace→KILL sequence every other cancellation path in
   this codebase already uses) rather than inventing a second one. Not a change to
   `internal/executor/local` itself — `Wait`'s contract (block until the process the caller already
   started exits) is correct and used correctly elsewhere; the gap was in assuming `ctx` bounded it
   without checking.
6. **`strategy: all` (05-gates.md's local default) is unconditional in this document**: every
   declared gate on a node runs regardless of an earlier one failing, and every failing gate's
   findings are concatenated into one `NodeGatesEvaluated`. A constitution-level
   `strategy: fail-fast` override does not exist yet (no constitution — decision #1) and is
   explicitly out of scope, not silently defaulted away from the doc's own stated preference.
7. **`waivable: false` is enforced by omission, not by a runtime check.** There is no
   `waiver.grant` code path anywhere in this engine — L11's job, per `05-gates.md`'s own
   "`waiver.grant` is deny-tier for every non-human principal" line — so a failed, `waivable: false`
   gate's `Result{Passed: false}` has no mechanism that could ever flip it back to `true`. The
   `Waivable bool` field on `GateDef` exists today purely as a declared, tested invariant
   (`TestEngine_waivableFalseGateFailureIsNeverSilentlyPassed` proves the absence of a bypass, which
   is the correct thing to assert about a control with no override mechanism to test against).
8. **The findings adapter is keyed by `findingsFrom: { format: "..." }` on the gate definition**,
   with exactly one format implemented (`golangci-json`). An unrecognised or unset format returns
   `nil` from `findingsFrom`, and the caller falls back to one synthetic finding built from the
   exit code and capped stdout evidence — never a panic, never a silently empty findings list on a
   failed gate (`domain.NodeGatesEvaluated`'s own invariant, enforced since L01, requires
   `len(Findings) > 0` whenever `Passed == false`).
9. **Gate evaluation output is read back from the event log, not threaded through `CmdEvaluateGates`.**
   The `Cmd` stays minimal (`RunID, NodeID, ExecID` only, matching `CmdStartNode`'s own
   routing-only shape) rather than growing an `Output json.RawMessage` field that would duplicate
   what `NodeOutputReceived`/`NodeWaitResolved` already recorded. `readExecOutput` scans the run's
   own stream for the most recent output event matching the exec, resolving an `OutputRef` via the
   artifact store (L09) exactly like `reapShell`/`reapLLM` already do — idempotent and replay-safe
   by construction (AGENTS §4 rule 3): re-running gate evaluation after a crash re-reads the same
   recorded fact rather than depending on anything held in memory.

## Public interfaces

```go
// internal/registry, additive
type GateKind string // GateExpr | GateCommand
type GateDef struct {
	ID, Kind, Severity, Message string
	Waivable                    bool // default true
	Expr                        string
	Command                     []string
	Workdir                     string
	ExpectExitCode              int
	Timeout                     time.Duration
	FindingsFormat              string
}
// Definition gains: Gates map[string]GateDef

// internal/constraint
type Evaluator struct{ /* unexported */ }
func New(exec local.Executor, admit *admission.Manager) *Evaluator
type Input struct {
	Gate                   registry.GateDef
	RunID, NodeID, ExecID  string
	Output                 map[string]any
	WorkDir, Dir            string
}
type Result struct {
	Passed              bool
	Reason              string
	ExitCode            int
	DurationMs          int64
	Findings            []domain.Finding
}
func (e *Evaluator) Evaluate(ctx context.Context, in Input) (Result, error)

// internal/domain, additive event
type ConstraintEvaluated struct {
	RunID, NodeID, ExecID string
	GateID, Kind          string
	Passed                bool
	ExitCode              int
	DurationMs            int64
	Reason                string
}
```

## Files to create

```
internal/registry/gates.go  gates_test.go

internal/constraint/constraint.go  expr.go  command.go  findings.go
internal/constraint/expr_test.go  command_test.go  findings_internal_test.go

adr/0013-expr-lang-for-expr-gates.md

# modified:
internal/registry/definition.go  defaults.go  validate.go
internal/domain/event.go  advance.go
internal/events/init.go  registry.go  fixtures_test.go  registry_test.go
internal/events/schemas/constraint.evaluated/v1.json  (new)
internal/events/fixtures/constraint.evaluated/v1.json  (new)
internal/executor/local/lookpath.go  (new — the one non-executor caller of exec.LookPath's
  binary-resolution need, wrapped so internal/constraint never imports os/exec itself)
internal/engine/engine.go  gates.go
internal/engine/gates_test.go  gates_expr_test.go  gates_waivable_test.go  (new)
adr/README.md
go.mod  go.sum  (github.com/expr-lang/expr)
```

## Data changes

None beyond L02's schema. `constraint.evaluated` reuses the `events` table like every event type
since L01.

## Acceptance criteria

- `go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run` clean; `go test ./... -race`
  green across every package, including the pre-existing `internal/registry` tests built on
  `fix-issue.yaml`'s unresolved gate names (decision #2 — proves the scope-narrowing didn't break
  the canonical example).
- All nine architecture tests pass; `internal/executor/local` remains the only package importing
  `os/exec`/`syscall`/`golang.org/x/sys` — `LookPath`'s wrapper lives inside that package, not a
  new exemption.
- `make cross` builds all four platform/arch combinations; `make arch` passes.
- A real `command` gate spawns through `internal/executor/local`, requests and releases a
  `cpu.heavy` admission permit, resolves its binary at an absolute preflight path, and records
  `constraint.evaluated` for both a passing and a failing outcome
  (`TestEvaluate_commandPassesOnMatchingExitCode`, `TestEvaluate_commandFailsOnMismatchedExitCode`).
- A failing gate on a real shell node, driven through the full engine end-to-end, loops back to
  the same node bounded by the workflow's `limits.loopGuard.maxIterationsPerNode`, then parks with
  `ParkLoopGuardExceeded` and non-empty `Findings`
  (`TestEngine_failingCommandGateLoopsThenParksUnderLoopGuard`).
- A `waivable: false` gate's failure is never observably bypassed anywhere in the engine
  (`TestEngine_waivableFalseGateFailureIsNeverSilentlyPassed`).
- No `TODO`, `FIXME`, or commented-out code in the diff.

## Tests

- `internal/constraint/expr_test.go`: passes/fails against real typed output; a missing field
  fails safely with a Reason, never a panic; invalid expression syntax fails safely.
- `internal/constraint/command_test.go`: exit-code comparison both directions; preflight
  binary-not-found fails loudly; ring-buffer evidence capping under a script that writes ~200 KiB;
  the `cpu.heavy` permit is genuinely requested and its denial genuinely fails the gate; a real
  `Timeout` actually kills a long-running command (proving decision #5's fix, not just the
  original — unfixed — assumption).
- `internal/constraint/findings_internal_test.go`: `golangci-json` decodes real issues into
  findings with a severity default; an empty report is not an error; malformed input returns an
  error, never a panic; an unrecognised format returns `nil`.
- `internal/registry/gates_test.go`: `Waivable` defaults true and an explicit `false` is
  preserved; an unsupported `kind` and an empty `command` are rejected at publish; an absolute
  `workdir` is rejected at publish; a node's unresolved gate reference (decision #2) still
  publishes cleanly.
- `internal/engine/gates_expr_test.go`, `gates_test.go`, `gates_waivable_test.go`: three real,
  full engine end-to-end scenarios (passing expr gate → success; failing command gate → bounded
  loop → park; a `waivable: false` gate can never be bypassed), each against the real
  `internal/executor/local`, not a fake.

## Benchmarks

None. `expr` evaluation is µs-scale by design (`05-gates.md`'s own placement table) and not yet
exercised at a volume where its cost matters; `command` gate cost is dominated by the spawned
subprocess, not this document's own code.

## Migration

None from a prior version.

## Future work

- `file`, `regex`, `git-diff`, `coverage`, `judged` gate kinds — `05-gates.md`'s constraints slice 2.
- The three non-code domain gate kinds (`13-domains.md`).
- The real constitution resolution/merge system (decision #1), waiver grant/approval, policy
  (`~/.kairos/policy.yaml`), effect confirmation, quorum judging — all L11 and later.
- Full artifact-store integration for a command gate's complete stdout/stderr, not just the 64 KiB
  evidence tail currently inlined into a synthetic finding.
- `strategy: fail-fast` and any per-gate-schedule override of `strategy: all` (decision #6).
- A dedicated `cpu.heavy` admission pool config knob, if reusing `ModelClass` (decision #4) proves
  confusing in practice or genuinely needs different sizing semantics from real model-class pools.
- Extending gate-level admission denial/timeout into `internal/admission`'s existing queue
  machinery, if `cpu.heavy` contention under real concurrent gate load turns out to matter more
  than the current immediate-fail behavior.
