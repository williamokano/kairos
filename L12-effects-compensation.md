# L12 — Effects + compensation

## Depends on

L09 (artifacts + logs), L10 (constraints + gates), L11 (policy + secrets), L13 (human decisions) —
all committed. L13 before L12 is deliberate: "human decisions ship before effects... the confirmation
path must exist before the first destructive effect provider does" (`12-build-plan.md`). This document
also closes the gap L11 and L13 each explicitly left for it: L11's synchronous `checkEffects` stub and
L13's human-task/weight machinery, both named in their own Future work sections as waiting on L12's
real effect dispatch to become the async pause/resume flow 05-gates.md describes.

## Scope

**In.**
- A new `internal/effect` package: `Provider` interface (`Kind`/`Attempt`/`Probe`/`Compensate`), two
  real builtins (`GitPush`, `GHPRCreate`) spawned through `internal/executor/local` — the same
  chokepoint law every prior document has upheld — and `IdempotencyKey(runID, nodeID, effect)`.
- A new `actor: effect` node kind in `internal/engine` (`dispatchEffectActor`): the node *is* the
  effect, declaring exactly one builtin via its existing `effects: []string` field and static
  arguments via a new `with:` block (`registry.NodeDef.With`).
- The full `effect.attempted` → `effect.applied`/`effect.failed`/`effect.unknown` state machine —
  six new additive `internal/domain` events (`EffectAttempted`, `EffectApplied`, `EffectFailed`,
  `EffectUnknown`, `EffectSimulated`, `EffectCompensated`), folded as no-op audit facts exactly like
  L05–L11's own additive events.
- The real async confirm/park/resume flow replacing L11's synchronous `checkEffects` stub: two more
  new domain events (`EffectConfirmationParked`, `EffectConfirmationAnswered`) with real
  `ExecPending → ExecWaiting → ExecPending` transitions in `internal/domain/advance.go`, and
  `Engine.Approve`/`Engine.AnswerEffectConfirmation` — `kairos approve` (L13's verb, reused, no new
  CLI surface) resumes a parked effect confirmation exactly as it resumes a `wait: human` decision.
- Kill-mid-effect recovery: `internal/engine/reconcile.go`'s `reconcileEffectNode`, probing an
  unresolved `EffectAttempted` by idempotency key instead of process-liveness (a git/gh subprocess has
  almost always already exited by the time a restart happens — what matters is whether the *external*
  mutation landed).
- Reverse-order compensation (`internal/engine/compensate.go`'s `compensateRun`), triggered from
  `shard.go` the moment a run's fold newly reaches `Cancelled` or `Failed`.
- Dry-run (`Engine.Config.DryRun` → `effect.simulated`) and an unattended-effect ceiling
  (`Engine.Config.UnattendedEffectCeilings`, wired to `config.Config.MaxUnattendedPRs`).
- Nine new limitation-register entries (`11-limitations.md`): NL-35 marked **RESOLVED by L12**
  (superseded, not deleted, per AGENTS §8), and NL-37 through NL-40 registering this document's own
  real narrowings.

**Out.** `kairos waiver grant`/`kairos effects confirm` as separate new CLI verbs — `kairos approve`
already covers the confirmation-answering surface end to end, reused rather than duplicated. TUI/web
rendering of decision cards or effect previews (later documents). Any effect provider beyond
`git.push`/`gh.pr.create` (Slack, Telegram, etc. are domain-layer/Phase-2 concerns). Dynamic
input-binding into `with:` (NL-37 — static values only). Per-run dry-run/ceiling scoping (NL-38 —
engine-wide only). `git.push`'s own compensation (NL-39 — deliberately absent, not merely unbuilt: no
declared revert action exists for a force-reverting push, and this document does not invent one). A
compensation retry queue (NL-40 — best-effort, once, logged on failure).

## Documented decisions

1. **`actor: effect` is a fourth actor kind, not a metadata tag on `shell`/`llm` nodes.** The node
   itself performs the builtin — the exact interpretation `05-gates.md`'s step-6 wording implies
   ("exec the builtin with a credentialed env"), distinct from a shell node whose *own* command
   happens to run `git push`. Validated at publish time: `actor: effect` requires exactly one entry in
   `effects: []string` (`internal/registry/validate.go`).
2. **Effect arguments are a new static `with:` block, not the existing `inputs` mechanism.** No actor
   in this codebase resolves `NodeDef.Inputs` at dispatch time yet (engine-level input-binding does not
   exist for any actor) — reusing `inputs`'s shape without a resolver behind it would silently do
   nothing. `with:` is deliberately the narrower, honest primitive: a flat `map[string]string` of
   literal YAML values, parsed and validated, with dynamic binding registered as NL-37 rather than
   half-built.
3. **The async confirm/park/resume flow needed two genuinely new `internal/domain` transitions, not a
   reuse of `wait: human`.** A `wait:`-declaring node's `NodeExecution` enters `ExecWaiting` directly
   from `dispatchExec`, bypassing `CmdStartNode`/`dispatchStartNode` entirely (`advance.go`'s
   `dispatchExec` doc comment) — architecturally incompatible with `checkEffects`'s job of intercepting
   an *already-Pending*, non-`wait:`-declared node before its actor ever runs. `EffectConfirmationParked`
   (`ExecPending → ExecWaiting`, dispatching `CmdCreateHumanTask` — reusing that Cmd exactly, since it's
   already "insert a human task") and `EffectConfirmationAnswered` (`ExecWaiting → ExecPending`,
   re-dispatching `CmdStartNode` with the same `ExecID`/`Attempt`/`Iteration` on approval, or routing via
   the failure edge — reusing `handleFailureOutcome` — on decline) are the new, minimal, legal transitions
   this requires. `EffectConfirmationRequested`/`EffectConfirmed` (L11) are kept unchanged as audit-only
   facts rather than repurposed — AGENTS §4 rule 6 requires new semantics on an existing shipped event
   type to be a new type with an upcaster, not a silent behavior change to an old one.
4. **"RELEASE ALL PERMITS" is trivially satisfied by ordering, not by an explicit release call.**
   `checkEffects` runs *before* `admitOrQueue` in `dispatchStartNode` (L07's admission step) — a parked
   node was never granted an admission claim in the first place, so there is nothing to release. Proven,
   not merely asserted: `TestEngine_parkedEffectConfirmationHoldsNoAdmissionSlot` caps `NodeSlots: 1` and
   shows an independent run's effect node is still admitted and completes while the first stays parked.
5. **`kairos approve` (L13's existing verb) answers both `wait: human` decisions and parked effect
   confirmations — no second CLI verb.** `Engine.Approve` tries `AnswerHumanTask` first; a new sentinel,
   `ErrNotAWaitHumanNode`, is the signal to fall through to `AnswerEffectConfirmation` instead. Keeps
   L13's anti-rubber-stamp discipline (`--reason` required, no `--yes`/`--all`) as the single enforcement
   point for every kind of parked confirmation rather than duplicating it.
6. **Kill-mid-effect reconciliation bypasses the generic process-liveness verdict switch entirely for
   `actor: effect` nodes.** `reconcile.go`'s `reconcileRun` special-cases `nd.Actor == "effect"` before
   reaching `local.ReadProcRecord`/`local.Probe` — a git/gh subprocess has almost certainly already
   exited by the time a restart happens; the question that matters is whether the *external* mutation
   landed, answered by `Provider.Probe(idempotencyKey)`, never by re-attempting.
7. **`effect.unknown` blocks a run's terminal status by absence of a fold, not by a new `RunStatus`
   value.** No `NodeExecutionFailed`/`NodeOutputReceived` is folded when a probe is inconclusive — the
   `NodeExecution` row simply stays `Executing` forever (until an operator resolves it, a callable core
   with no CLI verb yet, named as Future work), which is what `domain.RunState.Terminal()`'s
   derived-from-every-execution's-own-terminality logic already makes "blocks reaching Failed" mean for
   free, with no new domain concept required.
8. **A crash before `EffectAttempted` was ever recorded is treated as an ordinary `Lost` node, not as
   effect-specific.** Nothing was externally attempted, so the standard retry-or-route ladder
   (`recoverLost`, unchanged from L05/L06) is exactly correct and safe — no probe needed, because there
   is nothing to probe.
9. **Reconcile-time folding of `EffectConfirmationParked`/the effect state machine's terminal events
   captures `RunState` *before* the causing event is appended, never after.** `eventstore.AppendIf`
   already folds an event transactionally into the store's own projection as part of recording it
   (L02); reloading state *after* an append and calling `domain.Advance` again on it would apply the
   same event twice — the exact bug `TestEngine_effectActorSucceedsAndRecordsTheFullStateMachine` and
   `TestReconcile_effectAttemptedProbesAppliedAndSucceeds` caught during development (a run stuck at
   `Running` because the retried effect's `NodeOutputReceived`/`CmdEvaluateGates` was never
   dispatched). `foldAndDispatch`/`appendAndFoldBeforeStart`/`finishEffectNode` are the fix: capture
   `preState`, append, fold `preState` (never a fresh reload) against the just-appended event, dispatch
   the resulting Cmds. Used identically by `parkForEffectConfirmation`, `reconcileEffectNode`'s
   Applied/Failed branches, and `dispatchEffectActor`'s own terminal events when `!e.isLive()` (a retry
   dispatched from `Reconcile`, before `Start`'s live Subscribe loop exists to pick up an append-only
   event the way L13/L14's own `AnswerHumanTask`/`resolveConversationWait` rely on).
10. **`git.push` has no compensation; `gh.pr.create`'s is `gh pr close`.** 05-gates.md's own
    confirmation-preview example declares `gh pr close <n>` as `gh.pr.create`'s revert action but names
    none for a push — force-reverting a ref this system just pushed to, with no human confirming the
    reversal, is exactly the destructive-without-a-human-decision pattern AGENTS §4 rule 7 forbids.
    `GitPush.Compensate` returns `effect.ErrNotCompensable` unconditionally; `compensateRun` treats that
    (and any other `Compensate` error) as best-effort-failed: logged, left applied, never retried
    (NL-39, NL-40).
11. **Compensation is per-run, triggered once, on the newly-terminal `Cancelled`/`Failed` transition —
    never re-triggered, never a background sweep.** `shard.go`'s `process()` compares `before.Status`
    against `after.Status` so this fires exactly once per run, matching AGENTS §4 rule 1's "no work
    invented without a trigger" (L15) — compensation is a direct consequence of the specific event that
    made the run terminal, not a periodic scan.

## Public interfaces

```go
// internal/effect
type Outcome string // Applied | Failed
type Request struct {
	RunID, NodeID, ExecID string
	Effect, IdempotencyKey, WorkDir, Dir, PathPrefix string
	Args map[string]string
}
type Result struct { Outcome Outcome; ExternalRef, Reason string }
type Provider interface {
	Kind() string
	Attempt(ctx context.Context, req Request) (Result, error)
	Probe(ctx context.Context, req Request) (result Result, ok bool, err error)
	Compensate(ctx context.Context, req Request, externalRef string) error
}
var ErrNotCompensable error
func IdempotencyKey(runID, nodeID, effect string) string

type GitPush struct{ Exec local.Executor }     // Kind() == "git.push"
type GHPRCreate struct{ Exec local.Executor }  // Kind() == "gh.pr.create"
type Fake struct{ /* in-memory double, never a real network call */ }
func NewFake(kind string) *Fake

// internal/engine, additive Config fields
EffectProviders          map[string]effect.Provider // nil -> real GitPush/GHPRCreate
DryRun                   bool
UnattendedEffectCeilings map[string]int

func (e *Engine) Approve(ctx context.Context, runID, nodeID string, ans AnswerDecision) error
func (e *Engine) AnswerEffectConfirmation(ctx context.Context, runID, nodeID string, approved bool, reason string) error

// internal/domain, additive events
type EffectAttempted struct{ RunID, NodeID, ExecID, Effect, IdempotencyKey string }
type EffectApplied struct{ RunID, NodeID, ExecID, Effect, ExternalRef string }
type EffectFailed struct{ RunID, NodeID, ExecID, Effect, Reason string }
type EffectUnknown struct{ RunID, NodeID, ExecID, Effect string }
type EffectSimulated struct{ RunID, NodeID, ExecID, Effect string }
type EffectCompensated struct{ RunID, NodeID, ExecID, Effect, ExternalRef string }
type EffectConfirmationParked struct{ RunID, NodeID, ExecID, Effect string }
type EffectConfirmationAnswered struct{ RunID, NodeID, ExecID string; Approved bool; Reason string }

// internal/registry, additive NodeDef field
With map[string]string // actor: effect's static args, from a node's `with:` block
```

## Files to create

```
internal/effect/effect.go  git.go  gh.go  fake.go
internal/effect/git_test.go  gh_test.go

internal/engine/effect.go  compensate.go
internal/engine/effect_test.go  effect_admission_test.go  compensate_test.go  reconcile_effect_test.go

# modified:
internal/domain/event.go  advance.go  transitions.go
internal/events/init.go  registry.go  fixtures_test.go  registry_test.go
internal/events/schemas/effect.{attempted,applied,failed,unknown,simulated,compensated,confirmation.parked,confirmation.answered}/v1.json
internal/events/fixtures/effect.{attempted,applied,failed,unknown,simulated,compensated,confirmation.parked,confirmation.answered}/v1.json
internal/registry/parse.go  definition.go  defaults.go  validate.go
internal/engine/dispatch.go  engine.go  shard.go  reconcile.go  policy.go  policy_test.go  human.go
internal/api/human.go
internal/config/config.go
cmd/kairos/serve.go
11-limitations.md
```

## Data changes

None beyond L02's schema — eight new additive event types on runs' own existing streams, no new SQL
table, following L05–L11's exact pattern.

## Acceptance criteria

- `go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run` clean; `go test ./... -race`
  green across every package, including `cmd/kairos`'s real-binary flagship tests.
- All nine architecture tests pass on a clean run.
- `make cross` builds `CGO_ENABLED=0` for darwin/linux × arm64/amd64.
- `TestEngine_effectActorSucceedsAndRecordsTheFullStateMachine`/`...FailureIsRecorded` prove the
  `attempted → applied|failed` state machine against a fake provider double.
- `TestEngine_effectConfirmParkThenApproveResumesAndRunsTheEffect` proves the real async flow: the
  provider is called zero times before `Approve`, exactly once after.
- `TestEngine_effectConfirmDeclineRoutesToFailure` proves a decline never reaches the provider.
- `TestEngine_parkedEffectConfirmationHoldsNoAdmissionSlot` proves decision #4 with a real capacity
  constraint (`NodeSlots: 1`), not just by code inspection.
- `TestReconcile_effectAttemptedProbes{Applied,Failed}...` and
  `...UnprobeableRecordsUnknownAndBlocksTerminal` prove kill-mid-effect recovery for all three
  outcomes, including that `effect.unknown` genuinely leaves the run non-terminal.
- `TestReconcile_effectNeverAttemptedIsTreatedAsLostAndRetried` proves decision #8.
- `TestEngine_reverseOrderCompensationOnFailure` proves multi-effect reverse-order compensation against
  two independent fake providers, asserting call order via `ExternalRef`.
- `TestEngine_compensationLeavesNonCompensableEffectsAppliedWithoutError` proves decision #10/#11's
  best-effort semantics.
- `TestEngine_effectDryRunNeverCallsTheProvider` and `TestEngine_unattendedCeilingBlocksPastTheCap`
  prove dry-run and the ceiling are real, enforced gates, not documentation.
- `internal/effect/git_test.go`/`gh_test.go` prove the real providers against real local git
  fixtures (no network) and a fake `gh` binary stub (never a real network call), covering
  Attempt/Probe/Compensate for both.
- No `TODO`, `FIXME`, or commented-out code in the diff.

## Tests

Listed above under Acceptance criteria; also: `internal/registry`'s existing suite continues to pass
unchanged (the new `With`/actor-effect validation is additive, not a behavior change to any existing
actor kind), and `internal/engine/policy_test.go`'s
`TestEngine_confirmTierEffectParksWithoutARecordedConfirmation` replaces L11's
`...BlocksWithoutARecordedConfirmation` — the intentional, documented behavior change decision #3
makes (a real park, not an immediate failure).

## Benchmarks

None. Effect dispatch is a bounded external call (a `git`/`gh` subprocess), not a hot path this
codebase benchmarks at any layer.

## Migration

None from a prior version. NL-35 is marked RESOLVED (superseded per AGENTS §8), not deleted.

## Future work

- NL-37: dynamic input-binding into `with:` from an upstream node's output, once any actor gets a
  real engine-level input-resolution mechanism.
- NL-38: per-run `--dry-run`/unattended-ceiling scoping (currently engine-wide), requiring a flag
  threaded through `TriggerReceived`/`POST /runs`/the CLI.
- A `kairos effects` CLI surface (`kairos run effects <run>` — "what has been applied and what
  compensation would unwind" per 05-gates.md) — the daemon-side data exists in the event log; only the
  CLI verb itself is unbuilt.
- `EffectUnknown` resolution has a callable core (`internal/engine`, unexported today, reachable only
  by extending `recoverLost`'s pattern) but no CLI verb — an operator cannot yet resolve a blocked
  `effect.unknown` node without direct event-store access.
- NL-39/NL-40: a `compensation.failed` domain event distinct from silent-absence, and a manual
  `kairos effects compensate <run> <node>` retry verb, if compensation failures prove common in
  practice.
- Additional effect providers (Slack, Telegram, Jira transitions) — domain-layer/Phase-2 concerns per
  `13-domains.md`, not this document's `git`/`gh` scope.
