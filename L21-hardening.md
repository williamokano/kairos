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

Committed as `<pending>`.
