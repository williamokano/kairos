# L21 — hardening pass

Not a numbered build document (the plan ends at L20) — a running log of Future-work items from
L00-L20's own "Future work" sections and `11-limitations.md`'s NL-* register, picked up after the
full build plan shipped. Each entry: what was built, any real bug found and fixed.

## 1. NL-31 — close the illegal-transition trap at its remaining call sites (L07)

L07 fixed one instance of this bug (an admission `Denied` outcome tried to append
`NodeExecutionFailed` directly against a still-`Pending` exec, which `internal/domain`'s
`legalExecEvents` rejects — only `Executing` accepts `NodeExecutionFailed`) via a `denyNode`
helper that appends `NodeExecutionStarted` immediately before the failure. L07's own Future work
named the same trap as still open at `dispatchShellActor`'s and `dispatchLLMActor`'s pre-existing
early-failure returns.

Extracted the shared mechanism into `Engine.startThenFail` (`internal/engine/dispatch.go`) —
`denyNode`/`denyNodeWithReason` now call it too, rather than duplicating the append pair. Routed
every pre-start `appendNodeFailed` call in `actor_shell.go` (workspace-repo-missing, workspace
provisioning failure, process-start failure) and `actor_llm.go` (LLM-binary-missing, scratch-dir
creation, schema-file write, workspace provisioning, process-start) through `startThenFail`
instead. Found a fourth call site not named in L07's Future work but the identical bug:
`runActorDispatch`'s default case (an actor kind with no dispatch implementation) also called
`appendNodeFailed` directly against `Pending` — fixed the same way.

`denyNode`'s message carries a `"denied: "` prefix, appropriate for an actual admission/policy
refusal; the pre-start failures above are genuine runtime failures, not denials, so they call
`startThenFail` directly with an unprefixed message rather than borrowing `denyNode`'s wording.

**Real bug confirmed, not just theorized**: reverted the fix and ran the new test
(`TestEngine_preStartFailureNeverProducesAnIllegalTransition`,
`internal/engine/illegal_transition_test.go`) against the old code — it failed exactly as
predicted: `domain: illegal state transition`, and the run hung forever (`ExecPending` with no
recorded outcome, `RunRunning` forever, since nothing else moves it off that state). With the fix,
the same scenario reaches `RunFailed` with exactly one `ExecFailed` execution recorded.

Verification: `go build`/`go vet`/`gofmt -l .`/`golangci-lint run` clean, full `-race` suite green
(including a fresh, non-cached `cmd/kairos` run), `make cross` (4 platforms), `make arch` clean.

Committed as `05776a7`.

## 2. Daily-spend-window reset: a real day boundary, persisted across restarts (L07)

`admission.Manager.dailySpent` was a bare in-memory `float64` — reset to zero on every daemon
restart, and never reset on a genuine day rollover if the daemon stayed up across midnight.
Strictly more permissive than `02-config.md`'s real 24-hour `dailyUSD` window.

Added `admission.Config.Clock` (default `time.Now`, injectable for tests) and `OnSpendChange`
(called synchronously whenever the running total changes — a grant or a rollover reset), plus
`Manager.Today()`/`Manager.Seed(day, spentUSD)`. `TryAdmit` now checks the day key on every call
and resets to zero on a genuine rollover (local calendar date — `02-config.md` frames `dailyUSD`
as "your card," a single-user tool, not a fleet spanning timezones).

Persistence is a new `admission_spend` table (migration `0004_admission_spend.sql`, one row per
day) reached through `eventstore.Store`'s `GetAdmissionSpend`/`SetAdmissionSpend` — the same
shape as L16's `source_cursor` (a `source_cursor` reuse was considered and rejected: its
`source_id` column has a foreign key to the `source` table, which doesn't apply here and would
have been a forced fit). `internal/admission` itself still performs no I/O — `Engine.New` wires
`OnSpendChange` to a closure over `e.store`, and `Reconcile` seeds the Manager from the
persisted row for today before the live loop starts, mirroring the existing boot-sequence
pattern (reconcile-before-serve).

Tests: `internal/admission/day_boundary_test.go` proves both halves in isolation (same-day
restart resumes the persisted total; a genuine day rollover resets to zero, confirmed via
`OnSpendChange` and `Today()`) using a fixed injectable clock, no real sleeping across midnight.
`internal/engine/daily_spend_test.go` is the integration proof against a real SQLite store: a
node with `resources.model.maxCostUSD` runs through the real admission path, persists its spend,
and a second `Engine` over the same store — the shape of an actual restart — correctly denies a
run that would otherwise fit the cap, proving the restored total is genuinely enforced, not
silently reset.

Found one pre-existing, unrelated flaky test while running the full suite:
`TestEngine_spawnOnChildFailureDegradeStillSucceeds` failed once under full-suite `-race` load
("domain: illegal state transition" folding `node.wait.resolved`) but passed cleanly in
isolation and on 5 repeated full-package runs; this document's changes touch neither
`spawn.go` nor join/degrade logic, so it's flagged here as a known, apparently load-sensitive
pre-existing flake rather than chased down — out of this item's scope per AGENTS §7.

Committed as `c11f4bd`.

## 3. `kairos effects` CLI surface + `EffectUnknown` manual resolution (L12)

`L12-effects-compensation.md`'s Future work named two gaps: no CLI verb for "what has been
applied and what compensation would unwind" (the daemon-side data already existed in the event
log — `compensateRun` already walks it to decide what to reverse on cancel/failure), and no way
for an operator to resolve a node blocked in `effect.unknown` without direct event-store access
(`reconcileEffectNode`'s own `res.Outcome` switch was the only place this logic lived, reachable
only automatically during `Reconcile`).

Added `internal/engine/effects_list.go`: `Engine.Effects(ctx, runID) ([]EffectSummary, error)`
(a read-only replay of `EffectAttempted`/`Applied`/`Failed`/`Unknown`/`Compensated` events —
`WouldCompensateOnCancel` mirrors `compensateRun`'s exact applied-and-not-yet-compensated
selection, without ever calling a real provider) and `Engine.ResolveEffectUnknown(ctx, runID,
nodeID, outcome, reason)` — `reconcileEffectNode`'s exact append sequence
(`EffectApplied`/`Failed` then `NodeOutputReceived`/`NodeExecutionFailed`), made reachable
outside of `Reconcile` by following `isLive()`'s existing branch (live: `appendNext`, the shard
folds it; not live: `appendAndFoldBeforeStart`, same as `parkForEffectConfirmation`'s precedent).

New routes `GET /runs/{id}/effects` and `POST /runs/{id}/effects/resolve`
(`internal/api/effects.go`), `apispec.Op` entries, and CLI verbs `kairos effects <run>` /
`kairos effects resolve <run> --node --outcome --reason` (`internal/cli/effects.go`) —
deliberately no `--yes`/`--all`, matching `kairos approve`'s established anti-rubber-stamp
discipline, since resolving a node with no automated evidence either way is exactly the kind of
decision this codebase never lets be bypassed.

Tests: `internal/engine/effects_list_test.go` proves the listing against a real applied effect
and, separately, seeds the exact stuck-forever event sequence `reconcileEffectNode`'s
Probe-returns-false path produces (confirmed non-terminal before resolving, per this project's
consistent "assert the mess exists" discipline) and proves `ResolveEffectUnknown` unblocks it to
`RunSucceeded`. `cmd/kairos/effects_cli_test.go` is the real-daemon, real-binary proof: an
effect-free run's `kairos effects` returns a genuinely empty list, and `kairos effects resolve`'s
flag validation and the daemon's own invariant check (no Executing exec to resolve) both surface
correctly over the real HTTP wire, not just at the unit level.

No real production bug found in this item — everything built clean the first time it compiled;
the only fix needed was in the test file itself (an unused/broken helper from an earlier draft,
removed before committing).

Committed as `78f140d`.

## 4. `kairos waiver grant` / `kairos effects confirm` CLI verbs (L11)

`L11-policy-secrets.md`'s Future work named two callable-but-unreachable engine methods:
`GrantWaiver` (already enforcing "deny-tier for every non-human principal" and a required
reason, per `internal/engine/gates_waiver_test.go`'s existing tests) and
`GrantEffectConfirmation` (already exercised end-to-end by `internal/engine/policy_test.go`) —
both real, both fully tested at the engine level, neither reachable outside a direct Go call.

Added routes `POST /runs/{id}/waivers` and `POST /runs/{id}/effects/confirm`
(`internal/api/waiver.go`), `apispec.Op` entries, and CLI verbs `kairos waiver grant <run> --node
--gate --reason --ttl` and `kairos effects confirm <run> --node --effect --scope`
(`internal/cli/waiver.go`, a `confirm` subcommand added to `internal/cli/effects.go`) — deliberately
no `--yes`/`--all`/`--forever`, matching `kairos approve`'s discipline: `--ttl` is required (an
unexpiring waiver is exactly the permanent bypass a `waivable:false` gate exists to make
impossible, so this command refuses to offer one even for a `waivable:true` gate).

**Real bug found and fixed, the same class L14 already found once**: the first real end-to-end
test (`kairos waiver grant` against a live daemon evaluating a real `command` gate) failed with
`appending waiver.grant: exhausted retries on conflict` — `GrantWaiver` routed through
`appendNext`'s plain 5-retry CAS loop, which is correct for writes a single shard goroutine
already orders but provably too few for a write reachable from a **separate CLI process** racing
a live, busy run — exactly the profile `internal/conversation.AppendMessage` was already bumped
to 50 retries for (a real 20-way concurrent post burst, per L14). Fixed at the root: extracted
`appendNextRetrying(ctx, runID, ev, maxRetries)` from `appendNext`, added
`appendNextHumanFacing` (50 retries, mirroring conversation's exact reasoning), and routed every
external human-decision write through it — not just `GrantWaiver`/`GrantEffectConfirmation`
(this item's own scope), but also `AnswerHumanTask`'s and `AnswerEffectConfirmation`'s final
appends (L13/L12, same race profile, same latent risk, fixed alongside since it's the identical
bug rather than a different one) and item 3's own new `ResolveEffectUnknown`.

A second, purely test-design issue surfaced alongside: the first real end-to-end attempt granted
the waiver "immediately" after `kairos run` returned, matching the in-process engine test's own
timing — but that test's zero-latency in-process `GrantWaiver` call has no equivalent race
against a **separate OS process** (fork/exec/socket-connect overhead), and with
`maxIterationsPerNode: 1` the gate is evaluated exactly once, so a grant arriving after that one
evaluation is simply too late, structurally, not a bug. Fixed by giving the test node a `sleep 1`
before writing its output — gate evaluation only runs after `NodeOutputReceived`, so the grant
call has the whole sleep to land in.

Tests: `cmd/kairos/waiver_confirm_cli_test.go` — a real end-to-end proof that `kairos waiver
grant` against a live daemon unblocks a real, always-failing `command` gate (reusing
`gates_waiver_test.go`'s exact fixture), run three times to confirm the race fix holds, plus
table-driven CLI/daemon validation coverage for both verbs' required flags and value checks
(`GrantEffectConfirmation`'s own underlying logic is already exhaustively tested at the engine
level in `policy_test.go`, so a full local-git-remote round-trip for `effects confirm` was judged
not worth the added fixture weight here — the plumbing is structurally identical to the
already-round-trip-tested waiver-grant and `effects resolve` paths).

Committed as `<pending>`.
