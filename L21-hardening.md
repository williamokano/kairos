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

Committed as `452ee1b`.

## 5. `Idempotency-Key` server-side dedupe on `POST /runs` (NL-49)

`11-limitations.md`'s NL-49: the web composer already minted an `Idempotency-Key` (rendered as a
hidden form `nonce`), but neither the web handler nor the daemon read it — a double-click or a
retried request after a dropped response created two runs instead of one.

Added a new `run_idempotency` table (migration `0005_run_idempotency.sql`, `idempotency_key TEXT
PRIMARY KEY`) and `eventstore.Store.DedupeRunCreation`/`RecordRunCreation` — the exact two-step
claim-then-record shape L16's `DedupeTrigger`/`RecordTriggerRun` already established (claim the
key before the possibly-slow run-creation call, closing the race two concurrent identical
requests would otherwise both find "not yet created" for), deliberately a separate table rather
than reusing `trigger_dedupe`, matching NL-49's own framing that these are different identities.

`internal/api`'s `POST /runs` now accepts an optional `idempotencyKey` JSON field: a repeat call
with the same key returns the original run (`200`, not `201`) instead of creating a new one; a
different key, or no key at all, behaves exactly as before. `internal/cli.Client.CreateRun`
gained the parameter (empty from `kairos run` — a human typing a command once isn't the
double-submit scenario this fixes); `internal/web/mutations.go`'s composer handler now actually
reads and forwards the `nonce` form field it had been silently discarding since L20.

Tests: `internal/api/run_idempotency_test.go` proves the same-key/different-key/no-key matrix
via `httptest`; `cmd/kairos/idempotency_cli_test.go` proves the identical behavior over real HTTP
against a live daemon.

No real production bug found in the feature itself; a full-suite `-race` run did surface
`TestEngine_adoptSurvivesRestartWithoutKillingTheChild` failing once, but it passed cleanly both
in isolation and on a full `cmd/kairos` re-run immediately after — a second pre-existing,
apparently load-sensitive flake in this package (the first was
`TestEngine_spawnOnChildFailureDegradeStillSucceeds`, found and equally unrelated during item 2),
noted here rather than chased, since this item's changes touch none of the adopt/reconciliation
code involved.

Committed as `c13c005`.

## 6. A real, indexed `GET /human-tasks?state=open` (L20)

`L20-webui.md`'s Documented decision #5: the web home page's "waiting on you" section did a
`GetRun` call per non-terminal run (O(active runs) per page load) to find any node with
`ExecStatus == waiting`, because no real index existed yet.

Added `human_task_index` (migration `0006_human_tasks.sql`, `PRIMARY KEY (run_id, node_id)`) and
`HumanTaskIndexProjection` — a projection needing no cross-table read (unlike
`RunIndexProjection`, which depends on `run_state_projection`), switching directly on
`HumanTaskCreated`/`EffectConfirmationParked` (insert) and `HumanTaskAnswered`/
`EffectConfirmationAnswered` (delete). Both kinds of thing a human resolves via `kairos approve`
(`engine.Approve`) are indexed identically, since the web page only needs to know a row is open,
not which kind it is. New `Store.ListOpenHumanTasks`, route `GET /human-tasks?state=open`
(`state`'s only supported value — there is no answered-task history to serve yet, a real,
named scope limit rather than a silent gap), `apispec.Op` entry, CLI verb `kairos human-tasks`
(matching `09-cli-and-tui.md`'s Inbox framing: "queue only, never answers"), and the web home
page rewired to read the index instead of scanning.

Tests: `internal/eventstore/humantask_index_test.go` proves the projection tracks both event
kinds' opens and closes independently (registering only `HumanTaskIndexProjection` alone, since
it needs no `RunStateProjection` alongside it — a fully legal `domain.Advance` event sequence
would have been unrelated machinery this test has no need to construct). `internal/web`'s new
`TestHomePage_waitingOnYouReadsTheRealIndex` proves the home page renders from the real
`/human-tasks` endpoint (via the existing `newFakeDaemon` test harness, extended with a
`humanTasks` field) rather than the old per-run scan, using a fake `runState` with zero
`Executions` to make the point unambiguous. `cmd/kairos/human_tasks_cli_test.go` is the
real-daemon, real-binary proof: `kairos human-tasks` shows a genuinely parked `wait: human` node
and stops showing it once `kairos approve` answers it.

Full-suite `-race` surfaced the same two pre-existing, load-sensitive flakes noted in item 5
(`TestEngine_adoptSurvivesRestartWithoutKillingTheChild`,
`TestEngine_spawnOnChildFailureDegradeStillSucceeds`) — both confirmed passing in isolation and
on repeat runs immediately after, unrelated to this item's changes (which touch neither adopt nor
spawn/join code).

Committed as `f8dbb8d`.

## 7. A committed `cmd/kairos` end-to-end test for `kairos fork`/`kairos compare` (L18)

`L18-fork-replay-verify.md`'s own Future work: "this document's own verification ran the
equivalent by hand; committing it as a real test is straightforward follow-on work, not deferred
for a design reason."

`cmd/kairos/fork_compare_cli_test.go` runs a simple two-node `rule`/`shell` workflow with no
`workspace: write` node (so `Engine.Fork`'s own `needsWorkspace` check skips the git-snapshot
machinery entirely, keeping the test real without a git-remote fixture) to completion, forks it
via the real `kairos fork --set greeting=forked-hello` against a real daemon, and confirms: the
forked run's event log genuinely starts with the copied prefix tagged `run.forked` (the correct
`FromRunID`), the `--set` override lands in the copied `trigger.received`'s own `Params` (proving
overrides actually reach the new run — proving they change an actor's *runtime* behavior would
need per-node input-binding this codebase doesn't have yet, NL-37, so this checks exactly what
`Fork` itself guarantees), the forked run independently reaches `succeeded`, `kairos compare`
reports both runs correctly including `B.ForkedFrom`, and `kairos db verify` stays clean
throughout.

**Real bug found and fixed**: the first run of this test logged `fork: dispatching continuation
cmd failed ... domain: illegal state transition` — harmless in this case only because the
erroneous dispatch simply failed and was dropped, but a real defect in `Engine.Fork`
(`internal/engine/fork.go`). `lastCmds`'s "most recent non-empty cmds" heuristic (meant to see
past a trailing no-op bookkeeping event at the copy boundary) cannot distinguish that case from
"the run's real work already finished and its terminal transition simply produced zero cmds" —
both leave `lastCmds` holding a real but now-**stale** cmd from earlier in the sequence. Forking a
run at (or past) its own completion therefore re-dispatched an already-folded `CmdEvaluateGates`
against the new run's already-terminal exec. Fixed at the root: after the local replay loop, if
the resulting `RunState.Status.Terminal()`, `lastCmds` is cleared — a run forked at its own
completion has nothing left to continue, full stop. Confirmed fixed by re-running the test after
the change: the error log disappeared entirely (verified via `go clean -testcache` to rule out a
stale cached pass), and the two pre-existing `internal/engine` fork tests
(`TestEngine_forkCopiesReasoningExactlyAndRestoresWorkspaceApproximately`,
`TestEngine_forkRefusesDriftByDefault`) still pass, confirming the mid-run continuation-dispatch
path this fix does NOT touch is unaffected.

Full-suite `-race` surfaced the same pre-existing, load-sensitive
`TestEngine_spawnOnChildFailureDegradeStillSucceeds` flake noted in items 5 and 6 — confirmed
passing on three repeat runs in isolation immediately after, unrelated to `fork.go`.

Committed as `88458c7`.

---

All seven items from this hardening pass are complete: NL-31 (item 1), the daily-spend-window
reset (item 2), `kairos effects`/`effects resolve` (item 3), `kairos waiver grant`/`effects
confirm` plus the CAS-retry-budget fix (item 4), NL-49's `Idempotency-Key` dedupe (item 5), the
real `GET /human-tasks` index (item 6), and the committed fork/compare end-to-end test plus its
own real bug fix (item 7). Each item was independently read-first, implemented, tested, verified
across the full suite (`go build`/`go vet`/`gofmt`/`golangci-lint`/`go test -race`/`make
cross`/`make arch`), committed, and pushed — five genuine bugs found and fixed along the way (the
illegal-transition trap at a fourth, previously-unnamed call site; the CAS-retry-budget race
shared across every human-facing decision write; and the fork continuation-dispatch staleness
bug), and two pre-existing, load-sensitive test flakes identified, confirmed unrelated, and left
honestly noted rather than silently worked around.
