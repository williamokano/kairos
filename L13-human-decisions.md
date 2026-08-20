# L13 — Human decisions

## Depends on

L14 (conversations), transitively L04/L03/L02/L01. Built against L00-L11 and L14, all committed and
green. L12 (effects + compensation) explicitly comes AFTER this document — the build plan's own
ordering rule: "human decisions ship before effects... the confirmation path must exist before the
first destructive effect provider does."

## Scope

**In.**
- `internal/engine/human.go`: `dispatchCreateHumanTask` (makes `CmdCreateHumanTask` real — it was a
  WARN-only no-op since L05, explicitly named "L13 scope" there), `AnswerHumanTask` (the one and
  only path `HumanTaskAnswered` is ever appended from), `checkDecisionWeight` (the `type`-tier typed
  word enforcement).
- `internal/engine/timers.go`: a real, non-persisted in-memory wait-timeout wheel — `armTimer`,
  `resolveWaitTimeout`, `rearmOutstandingTimers` — making `onTimeout: park`/`escalate` genuinely
  fire for the first time (L05's decision #6 left `CmdArmTimer` as a WARN-only no-op).
- `internal/registry`: `WaitDef.Weight` (`silent`/`glance`/`read`/`type`, defaulted to `read` for
  any `wait.on[].kind: human` entry with no explicit declaration), validated at publish time.
- `internal/api/human.go` + `internal/apispec` + `internal/cli/approve.go`: `POST
  /runs/{id}/approve` and `kairos approve`, deliberately without `--yes`/`--all`/`-f`.
- `internal/cli/run.go` + `internal/config`: `kairos run --unattended`, refused without
  `KAIROS_UNATTENDED_ACK` set to a `"yes-..."`-shaped value in config.

**Out.** Converting L11's synchronous `checkEffects` confirm-tier check into the full async
`effect.confirmation.requested → Waiting → resume` flow (Documented decision #1); resolving an
`ExecParked` human task (loop-guard-exceeded, wait-timeout-escalate, non-idempotent-at-boot) —
`HumanTaskAnswered` is legal only from `ExecWaiting`, and `ExecParked` is terminal by design;
glance-tier batching and first-time-target tier escalation (Documented decisions #3, #4); TUI/web
rendering of decision cards (later documents); connector-specific decision surfaces (Phase 2b).

## Documented decisions

1. **This document does NOT touch `internal/engine/policy.go`'s `checkEffects`.** L11 already named
   the overlap explicitly: converting the synchronous "fail immediately, re-run after
   `GrantEffectConfirmation`" check into the real `effect.confirmation.requested → human task →
   RELEASE ALL PERMITS → Waiting` state machine `05-gates.md` describes requires a NEW domain-level
   `Pending→Waiting` legal transition triggered by a runtime policy decision (not, as today, only by
   a workflow author's static `wait:` YAML declaration) — that is a real domain-model change, and it
   materially overlaps L12's effect-dispatch machinery. Per the build plan's own ordering note ("the
   confirmation path must exist before the first destructive effect provider does"), this document's
   job is to build the confirmation path itself — the human task queue, weights, the approve verb,
   real `onTimeout` firing — so it EXISTS and is real, even though effect-checking doesn't invoke it
   yet. L12 is expected to wire `checkEffects` through it once real effect dispatch exists.
2. **"Decision weight must match reversibility" is reframed onto `wait: human` nodes in general, not
   specifically onto effects.** `05-gates.md`'s tier table is written for effect confirmations, but
   per decision #1 there is no real effect-confirmation Waiting flow to attach weights to yet. The
   only real, live human-decision surface in this codebase today is the `wait: { on: [{kind:
   human}] }` "human-approval" node pattern (`03-workflows.md`), which was previously fully
   scaffolded at the domain layer (`HumanTaskCreated`/`HumanTaskAnswered`/`CmdCreateHumanTask` all
   existed since L01) but never actually wired to anything — `CmdCreateHumanTask` was a bare
   WARN-and-drop. `WaitDef.Weight` is declared on the wait node itself; L12 can extend the same field
   (or add an analogous one) to effect confirmations once that flow exists.
3. **Only the `type` weight has a real, testable CLI-layer enforcement.** `silent`/`glance`/`read`
   differ from each other only in how a future TUI would RENDER the decision (single keypress vs.
   full evidence panes vs. nothing at all) — a rendering concern this document, which builds no TUI,
   cannot meaningfully test. `type` is different: `05-gates.md` calls for "the typed decision word,
   full evidence, no batching," and the CLI-only surface this document owns already forces every
   decision to be a fully-typed `--confirm <word>` value (there is no keypress shorthand at any
   tier), so the ADDITIONAL requirement `type` weight adds — a second, distinct typed confirmation —
   is implemented as `--typed-confirm <nodeID>`: the caller must type the node's own id a second
   time. This is a narrowed, defensible proxy for "the typed decision word, full evidence" that is
   real and testable without inventing what the word itself should be (the doc doesn't specify one).
4. **Glance-tier batching and first-time-target escalation are NOT built.** Both require an
   "effect target" concept (a push branch name, a PR repository, a recipient) that does not exist
   anywhere in this codebase — `registry.NodeDef.Effects` is `[]string` of bare effect names
   (`gh.pr.create`), never targets. Implementing either rule meaningfully would require plumbing a
   target string through actor dispatch into policy — real work, but a different, larger document's
   scope (most naturally L12's, once effects actually execute and have something to target).
   Building a fake proxy (e.g., "first use of an effect NAME" instead of "first use of a target")
   would produce something that LOOKS like the security property `05-gates.md` describes without
   actually being it — worse than not building it, per AGENTS §7's "don't add scope because it
   looked easy." Named honestly as Future work instead.
5. **The wait-timeout wheel is in-memory, not persisted — the same documented gap L05's decision #6
   named for `CmdArmTimer`, now narrowed rather than closed.** A `time.AfterFunc` keyed by
   `(runID, nodeID, execID)` fires while the daemon process is up; it does NOT survive a restart on
   its own. What DOES survive a restart is `Start`'s own catch-up pass (`rearmOutstandingTimers`,
   called after `Reconcile`, before the live loop begins accepting new events): it scans every
   non-terminal run's `ExecWaiting` executions whose node declares a `WaitSpec.TimeoutAt`, resolves
   any already-overdue one synchronously (mirroring `reconcileRun`'s own `recoverLost` precedent —
   nothing else will ever act on it otherwise), and re-arms an in-memory timer for the rest. A daemon
   that is never restarted therefore gets exactly the timeout behaviour `03-workflows.md` describes;
   a daemon that restarts still fires every timeout, just not necessarily at the millisecond it was
   due if the restart itself was mid-overdue — which is the correct, honest reading of "a run parked
   on a human for three days is the system working correctly," not a regression. A full persisted
   timer wheel (a SQL table, armed/disarmed transactionally with the events that create/resolve a
   wait) remains genuinely Future work.
6. **`AnswerHumanTask` appends `HumanTaskAnswered` and returns — it does NOT also fold+dispatch the
   resulting `CmdEvaluateGates` itself.** The first implementation did, and a full-suite `-race` run
   caught the bug immediately: `AnswerHumanTask` is only ever reachable while the engine is live (per
   `cmd/kairos/serve.go`'s boot order, `Reconcile` completes, then `Start` begins the live loop, THEN
   the API socket binds), so the `human.task.answered` event it appends lands on the run's own
   stream — which the live `Subscribe` loop is already watching. The owning shard picks it up and
   dispatches `CmdEvaluateGates` itself; doing so a second time from `AnswerHumanTask` raced the
   shard's own dispatch and produced `domain: illegal state transition` (`node.gates.evaluated`
   folded against a `NodeExecution` the shard's own dispatch had already advanced past). This is the
   identical shape of bug `resolveConversationWait`'s `dispatchCmds bool` parameter was built to
   avoid (L14) — the fix here is simpler, since `AnswerHumanTask` has no Reconcile-time caller at
   all, so it can just never self-dispatch.
7. **`kairos run --unattended`'s acknowledgement check is entirely CLI-side, not daemon-side.**
   `05-gates.md`: "kairos run --unattended refuses unless config contains
   `unattended.iUnderstandEffectsWillNotBeConfirmed`." Since L11's `checkEffects` remains synchronous
   (decision #1) and this document builds no daemon-side unattended-mode behaviour change at all, the
   flag's entire job today is the safety gate itself — refuse to even issue the `POST /runs` request
   without `KAIROS_UNATTENDED_ACK` set locally to a `"yes-..."`-shaped value. `ErrUnattendedAckMissing`
   is returned before `ensureClient` is ever called, so no daemon needs to be running for the refusal
   to work. Once L12 gives confirm-tier effects a real async behaviour, unattended mode's actual
   run-time semantics (auto-deny vs. auto-park confirm-tier effects) become that document's job to
   wire — this flag's ack-string gate stays exactly as-is either way.

## Public interfaces

```go
// internal/engine
type AnswerDecision struct{ Decision, Reason, TypedWord string }
func (e *Engine) AnswerHumanTask(ctx context.Context, runID, nodeID string, ans AnswerDecision) error
var ErrHumanDecisionReasonRequired = errors.New(...)
var ErrHumanDecisionTypedWordRequired = errors.New(...)

// internal/registry, additive WaitDef field
const (
	WeightSilent = "silent"
	WeightGlance = "glance"
	WeightRead   = "read"
	WeightType   = "type"
)
type WaitDef struct {
	// ...existing fields...
	Weight string
}

// internal/api
POST /runs/{id}/approve   {nodeId, decision, reason, typedWord?}  -> 200 | 400 | 503

// internal/cli
kairos approve <runID> --node <id> --confirm <decision> --reason <reason> [--typed-confirm <word>]
kairos run --unattended <file>   // refuses without KAIROS_UNATTENDED_ACK
```

## Files to create

```
internal/engine/human.go  human_test.go
internal/engine/timers.go  timers_test.go

internal/api/human.go  human_test.go
internal/cli/approve.go  approve_test.go  unattended_test.go

internal/registry/waitweight_test.go

# modified:
internal/engine/dispatch.go   (CmdCreateHumanTask, CmdArmTimer real dispatch)
internal/engine/engine.go     (timers field, runCtx, rearmOutstandingTimers call, stopAllTimers)
internal/registry/definition.go  defaults.go  validate.go   (WaitDef.Weight)
internal/api/server.go        (Deps.Engine, registerHumanRoutes)
internal/apispec/ops.go       (POST /runs/{id}/approve)
internal/cli/client.go  root.go  run.go
internal/config/config.go     (UnattendedAck)
cmd/kairos/serve.go           (deps.Engine = eng)
```

## Data changes

None beyond L02's schema. `human.task.created`/`human.task.answered` are pre-existing event types
(registered since L01/L02) — this document is the first to actually append them outside a test
fixture.

## Acceptance criteria

- `go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run` clean; `go test ./... -race`
  green across every package, including `cmd/kairos`'s real-binary flagship tests.
- A `wait: { on: [{kind: human}] }` node genuinely suspends (does not proceed on its own),
  `human.task.created` is recorded, and `kairos approve`/`AnswerHumanTask` genuinely resolves it —
  proven end-to-end against the real live engine loop, not just the pure domain fold.
- `wait.weight: type` requires `--typed-confirm <nodeID>`; missing or mismatched is rejected with
  `ErrHumanDecisionTypedWordRequired`; the actor/caller cannot supply any parameter that weakens a
  node's declared weight — there is no such parameter to supply.
- `onTimeout: park` marks a Waiting execution `Overdue` without transitioning it; `onTimeout:
  escalate` transitions it to `ExecParked` with `ParkReason: wait-timeout-escalate` and records a
  second `human.task.created` — both proven by a REAL timer firing (a short real-clock wait, not a
  simulated fold), not just `handleWaitTimeout`'s pure-domain unit test.
- A restart correctly resolves an already-overdue wait timeout during `Start`'s catch-up pass, before
  the live loop begins accepting new events — proven by seeding an overdue `TimeoutAt` against a cold
  store and confirming `ExecParked` by the time `Start` returns.
- `kairos approve`'s flag set is grepped, not eyeballed, for the absence of `--yes`/`--all`/`-f`.
- `kairos run --unattended` refuses without a correctly-shaped `KAIROS_UNATTENDED_ACK`, with no
  daemon contacted in the process.
- `make cross` builds `CGO_ENABLED=0` for darwin/linux × arm64/amd64; `make arch` (9 architecture
  tests) clean.
- No `TODO`, `FIXME`, or commented-out code in the diff.

## Tests

- `internal/engine/human_test.go`: `TestEngine_humanApprovalNodeSuspendsThenAnswers` (full live-loop
  end-to-end), `TestAnswerHumanTask_reasonRequired`, `TestEngine_typeWeightRequiresTheTypedNodeID`.
- `internal/engine/timers_test.go`: `TestEngine_waitTimeoutParkOnlyMarksOverdue`,
  `TestEngine_waitTimeoutEscalateParksAndCreatesHumanTask`,
  `TestEngine_restartCatchesUpAnOverdueWaitTimeout`.
- `internal/registry/waitweight_test.go`: default resolution, explicit override, unknown-value
  rejection at publish time.
- `internal/api/human_test.go`: no-engine-configured 503, missing-fields 400.
- `internal/cli/approve_test.go`: flag-introspection proof of no rubber-stamp flags.
- `internal/cli/unattended_test.go`: missing/wrong-shaped ack both refused, no daemon contacted.

## Benchmarks

None. Nothing introduced here is on L02's durability-sensitive hot path.

## Migration

None from a prior version.

## Future work

- L12 (effects + compensation) is expected to replace L11's synchronous `checkEffects` confirm-tier
  check with the real async `effect.confirmation.requested → Waiting → resume` flow this document's
  human-task/weight/approve machinery was built to support (decision #1).
- Glance-tier batching and first-time-target tier escalation, once an "effect target" concept exists
  to batch/escalate on (decision #4).
- A persisted, restart-transparent timer wheel (decision #5) — the in-memory one here is real but not
  durable mid-countdown.
- Resolving an `ExecParked` human task (loop-guard-exceeded, wait-timeout-escalate,
  non-idempotent-at-boot) needs an operator action this document does not build — `ExecParked` stays
  terminal with no legal path back to a fresh attempt today.
- TUI/web rendering of the decision card and the full approval screen (`09-cli-and-tui.md`'s mockup)
  — later documents; this one produces only the daemon-side data/API surface and the CLI verb.
