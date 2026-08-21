# L23 — web UI revamp

Not a numbered build document (the plan ends at L20; L21/L22 are running hardening/integration
logs) — a running log, same shape as L21/L22, picking up items named in L20-webui.md's own Future
work one pass at a time.

**Pass 1** (sections 1–3 below) built the diff viewer and the compare page. Both were named there as
"the web rendering is missing" — `internal/cli.Client.Compare`/L18's `Engine.Compare` already
existed for compare; the diff viewer needed real daemon-side machinery too (nothing had ever
exposed a node's or a run's file-level change before this pass — `decision.gohtml`'s own CHANGED
pane said so plainly: "no diff data exposed by the daemon API yet").

**Pass 2** (section 4) built the event log explorer with a causal tree, and the findings/cost/
gates/sources/runners/flows pages — the next items L20-webui.md's Future work named verbatim.

## 1. The diff viewer

### Daemon side (new, not just rendering)

`internal/workspace/diff.go`: two new `Manager` methods, `DiffPatch`/`DiffNumstat`, both running
`git diff` through the same `runGitOutputEnv` plumbing `SnapshotGitRef`/`RestoreGitRef` already
use — no new git-invocation site, just new arguments to the existing one.

`internal/engine/diff.go`: `Engine.Diff(ctx, DiffRequest{RunID, NodeID})`. With no `NodeID`, it's
the whole run's change: the project's configured `BaseRef` (engine-wide, same one 05-gates.md's
git-diff/regex gate kinds already require) to the run's latest `workspace.snapshot.taken` SHA. With
a `NodeID`, it's that node's own before/after boundary: the *previous* snapshot in the run's
sequence (or `BaseRef`, if it's the first workspace-write node) to that node's *own* snapshot —
found by walking the run's `WorkspaceSnapshotTaken` events, exactly the record `Engine.Fork` already
reads for the identical reason. The node's declared `workspacePaths` (`registry.NodeDef`, resolved
the same way `resolveNode` already does for effects/gates) is compared against the changed-file list
for the scope-violation banner. Numstat and patch are two separate git calls, matched back up by
path — deliberately split, mirroring `internal/constraint/gitdiff.go`'s existing stat-call/
content-call division for the identical reason (a stable, parseable stat line vs. a full-text diff).

`internal/api/diff.go`: `GET /runs/{id}/diff?node=` — the api-package response struct (camelCase
JSON) is distinct from the engine's Go-named fields, matching `runSummaryForCompare`/`CompareSide`'s
existing convention exactly. `internal/cli.Client.Diff` mirrors it; `kairos diff <run> [node]` is
the new CLI verb, satisfying `TestUI_everyCallHasCLICounterpart`'s parity requirement for the new
`apispec.Op`.

### Web side

`internal/web/diffparse.go`: a small unified-diff parser (`parsePatch`) — reads `+++`/`---`/`@@`
headers and `+`/`-`/` ` prefixed lines only, tracking old/new line numbers per hunk. `buildSideRows`
turns one hunk's linear sequence into side-by-side rows: a context line occupies both columns; a run
of consecutive removals is zipped, position by position, against the run of additions that follows
— a real, named simplification (NL-51 below), not a full LCS realignment.

`internal/web/diffrender.go`: real server-side syntax highlighting via `github.com/alecthomas/
chroma/v2` — an **approved dependency named for exactly this** (AGENTS.md's table: "server-side
only, for the web diff viewer... no client-side highlighter, no 400 KB of JS, and no CSP
exception"), added to `go.mod` this pass (it was not there before — approved in the constitution but
never actually pulled in until a document needed it). `highlightLine` tokenises one diff line's
content and renders it via chroma's html formatter with `PreventSurroundingPre` so no `<pre>`/`<code>`
wrapper fights this package's own per-line markup. The stylesheet (`chromaStylesheet`, one fixed
dark style, `github-dark`) is generated from the pinned chroma version at first request and served
at `GET /static/chroma.css` — generated, not hand-vendored, so it can never name a class the running
binary's chroma version doesn't actually emit.

`internal/web/templates/diff.gohtml`: side-by-side (default) and unified modes, a full-width
scope-violation banner in the risk colour, per-file stat header, binary files collapsed with the
reason named. `?file=&line=` deep links resolve **server-side** to a 302 whose `Location` carries a
URL fragment matching a real `id` the page renders — no inline `<script>` at all (this page's
CSP is `script-src 'self'`, no `unsafe-inline`; a plain browser already jumps to a matching id
anchor on redirect with zero client script).

`decision.gohtml`'s CHANGED pane and `run.gohtml` now link to the real page instead of the
placeholder text.

## 2. The compare page

`GET /compare?a=&b=`: renders `internal/cli.Client.Compare`'s existing call verbatim — no new
comparison logic, exactly what L20-webui.md's Future work said was true ("the web rendering is
missing"). Cost is not rendered because `Compare` itself does not return one (NL-48, never durably
metered) — the page renders exactly what the daemon returns, it does not add a field. A drift
annotation (`ForkedFrom`/`Drifted`/`DriftDetail`) renders when one side is a fork of the other.
`apispec.Op`'s existing compare entry gained a `WebPath`.

## 3. A real bug found by this pass's own end-to-end test

Writing the compare page's real-daemon test (a forked pair with drift — the task's own required
scenario) surfaced a genuine, previously-latent bug, unrelated to rendering: **`Engine.Fork`
dispatches its continuation `Cmd`s synchronously using the *caller's* `ctx`.** That's fine when
`Fork` is called from a long-lived test context, but `POST /runs/{id}/fork`'s `ctx` is an
`*http.Request`'s context — cancelled by `net/http` the instant the handler returns, almost always
before a real dispatched node's child process finishes. Dispatching a shell/LLM node spawns a
background goroutine that **outlives** the request (`reapShell`'s `e.wg`-tracked watcher); every
`e.store.AppendIf` that watcher subsequently attempted (recording the node's real output) then
failed with `context.Canceled` — **silently**, since `reapShell` only checks that error to decide
whether to snapshot next, never logs it. The node sat `Executing` forever with no recorded outcome
at all, even though the process had genuinely already completed on disk (`output.json` present,
`stdout.log` clean, exit 0) — an invisible hang indistinguishable, from `kairos show`'s point of
view, from a node that never started.

Every *other* dispatch path avoids this: ordinary node dispatch runs from the engine's own
long-lived `Start(ctx)` loop, and `AnswerHumanTask` deliberately does **not** dispatch synchronously
at all (its own doc comment: relies on that same live loop, to avoid a double-dispatch race).
`Engine.Fork` is the one place that dispatches synchronously from a request handler — so it is the
one place that needed to detach.

**Fixed** with `context.WithoutCancel(ctx)` (`internal/engine/fork.go`) for exactly the
continuation-dispatch call — every value `ctx` carries is preserved, only its cancellation/deadline
is dropped, the same shape a signal handler uses to hand off work that must outlive the signal.

**Confirmed as a real bug, not a theorized one**: reverted the fix and ran the new regression test
(`TestEngine_forkSurvivesCallerContextCancelledRightAfterItReturns`,
`internal/engine/fork_context_test.go`) against the old code — it failed exactly as predicted
("run ... did not reach a terminal state within 10s"), reproducing the hang deterministically (a
node whose shell command sleeps 0.2s, forked with a caller context cancelled the instant `Fork`
returns — the identical shape an HTTP handler creates). With the fix, the same scenario reaches
`succeeded`. The real end-to-end test that found this
(`cmd/kairos/diff_compare_webui_test.go`) hung the same way against the real daemon before the fix
and passes cleanly after it.

## 4. The operations pages

The second pass through this log, picking up the next items L20-webui.md's Future work named
verbatim: "event log explorer with a causal tree" and "findings/cost/gates/sources/runners/flows
pages — each a filtered-table-plus-detail page per `10-webui.md`'s own description." Built by
reusing existing daemon routes wherever the data was already reachable, and adding new surface only
where it genuinely was not.

### Event log explorer (`/events`)

A filtered table over the daemon's real event log (`GET /events`, the same route `kairos logs`
already uses — no new daemon route) plus the causal tree `10-webui.md` names: `?focus=<seq>` walks
`CausationSeq` (carried by `internal/events.Envelope` since L02, decoded by a daemon client for the
first time here — `internal/cli.Client.Envelope` never had the field) up to its root ancestor and
recursively down through everything that event in turn caused. `internal/web/events.go`'s
`ancestryChain`/`descendantTree` compute this from the FULL fetched set regardless of `?type=`/
`?after=` — an ancestor or descendant of the focused event may sit outside whatever filter narrows
the visible table rows, so the tree is never accidentally pruned by an unrelated filter.

### Findings (`/findings`) and gates (`/gates`)

Both computed entirely from `constraint.evaluated` (L10/L11's `ConstraintEvaluated`) and
`waiver.grant` events read off the same `GET /events` route — no new daemon route for either page.
Findings defaults to `state=failed` ("what is still open"), `state=all` shows every evaluation.
Gates aggregates fires/failures/waivers per `GateID` into a catch-rate percentage and links each row
to its own filtered findings view. Neither page reads a static "declared gates" list, because none
exists: a flow's `gates:` block lives only in its own on-disk YAML, read fresh at run-creation time
(`internal/registry.Load`) — there is no durable gates registry to enumerate. A gate that has never
fired simply does not appear, which is the honest behavior, not a gap.

### Cost (`/cost`) — the first client-facing read of L07's admission spend

This is the one genuinely new daemon capability this pass added: before it, `internal/admission`'s
daily-spend cap tracker (`admission_spend`, persisted since L07) existed only inside the daemon's own
process/database — no route, no CLI verb, nothing had ever read it back. `internal/api/cost.go`'s
`GET /cost` (new `apispec.Op`, new `kairos cost` verb, satisfying `TestUI_everyCallHasCLICounterpart`)
returns today's persisted estimate against the configured `dailyUSD` cap. The page is deliberately
narrower than `10-webui.md`'s mockup ("spend by day / project / model / actor"): that breakdown does
not exist — `GetAdmissionSpend` keeps exactly one row, one calendar day, no history — and NL-30
(`11-limitations.md`) means the number itself is an admission-time ESTIMATE, never a reconciliation
against what a run actually cost. The page states this plainly rather than rendering a chart the
system cannot produce, and separately surfaces a count of `session.cost.unavailable` events (also
read via `GET /events`) as the honest complement to the estimate.

### Sources (`/sources`)

Reuses the existing `GET /sources` route/`kairos src ls` verb (no new `apispec.Op`), extended to
read back `08-triggers.md`'s cursor/health data "verbatim" as that document requires:
`internal/api/sources.go`'s `sourceResponse` gained `consecutiveErrors`/`lastPollAt`/`nextPollAt`/
`cursor` (a call to the existing `GetSourceCursor`), additive JSON fields that do not change the
route's shape for any existing caller. This pass is a read-only status view — the pause/resume/
poll-now dialogs `10-webui.md` also names for this page are out of scope here (the CLI path,
`kairos src pause|resume`, is unaffected and already real); see Future work.

### Runners (`/runners`)

Mirrors `internal/tui`'s own `viewRunners` exactly, down to the doc comment: one real `local` row
sourced from real `GET /doctor` data, nothing invented. `07-runners.md`'s remote-runner management
is a later, unbuilt phase; this page does not pretend otherwise.

### Flows (`/flows`)

The honest substitute for "a listing of published workflow definitions": there is no definitions
registry anywhere in this system — `internal/registry.Load` reads a YAML file straight off disk at
run-creation time, every time, and nothing durable ever calls a flow "published." What IS real and
durable is every run's own `TriggerReceived.DefinitionRef`, so this page groups by that instead —
computed from `GET /events` (`trigger.received`) cross-referenced with `GET /runs` for outcome, no
new daemon route.

### A real bug this pass's own review caught before it shipped

`internal/web/flows.go`'s first draft grouped runs by `DefinitionRef` via `runID -> DefinitionRef`
map, then range-ed over that map to decide both the flow list's display order and which run counted
as "first seen" per definition — Go deliberately randomizes map iteration order, so the flows page
would have rendered its rows in a different order on every single page load, with no test catching
it (map-iteration randomization is real but small enough that a single test run rarely surfaces it).
Fixed by building `order`/`byDef` directly off `envs` (already `GlobalSeq`-ordered) instead of
ranging over the intermediate map — deterministic across requests, and a repeated-run test
(`go test -run TestFlowsPage -count=5`) confirms the fix rather than merely asserting membership.

## 5. Cancel/fork/source-pause dialogs, the command palette, and full keyboard parity

The third pass through this log, closing L20-webui.md's Future work items "cancel/fork/say/
source-pause dialogs" and "command palette (⌘K) and full keyboard model parity with the TUI."

### A real gap found before any web code was written: `kairos cancel` did not exist

L20-webui.md's own Future work said, verbatim, "the CLI verbs and daemon routes all exist
(`kairos cancel`/`fork`/`say`/`src pause`); this pass's mutation set was scoped to
start/answer/message only." That was true for `fork` (L18) and `src pause` (L16) — **it was not
true for `cancel` or `say`**. Neither `internal/api`, `internal/cli`, nor `internal/cli.Client` had
ever had a cancel route, verb, or client method; `09-cli-and-tui.md`'s `kairos cancel <run>` and
`POST /runs/{id}/cancel` were prose, not code. Building the web cancel dialog therefore required
building the daemon capability first, not merely presenting an existing one — the one piece of new
engine work this pass did, and it was bounded: `domain.RunCancelled`, `advanceRunCancelled` (which
signals every in-flight node execution via `CmdSignalNode`), and shard.go's automatic
Running/Degraded → Cancelled compensation had existed since early in the build but were never once
exercised end-to-end — nothing had ever appended the event. `internal/engine/cancel.go`'s
`Engine.Cancel(ctx, runID, reason)` is the missing append; `internal/api/cancel.go`
(`POST /runs/{id}/cancel`), `internal/cli.Client.Cancel`, and `internal/cli/cancel.go`
(`kairos cancel <run> --reason "..."`) are the surface built on top of it. Cancellation always
compensates every applied effect — shard.go's handler is unconditional — so there is deliberately
no `--compensate` flag to thread through (09-cli-and-tui.md's own prose implies a toggle; the
domain event carries no field for one, and inventing one to match the prose would be exactly the
"implement your own design" AGENTS.md §6 forbids).

### A real, previously-latent domain bug this pass's own test found

`TestCancel_stopsARunningNodeAndReachesCancelled` (`internal/engine/cancel_test.go`) — a real shell
node sleeping, cancelled mid-flight — failed the first time it ran, not because of a test bug:
`advanceRunCancelled` flips `RunState.Status` to `RunCancelledS` synchronously, in the same
`Advance` call that produces `CmdSignalNode`; but `dispatchSignalNode` records
`NodeExecutionInterrupted` by appending a **new** event onto the same stream, which is folded by a
**separate**, later `Advance` call against the now-`Cancelled` state. `advanceNodeExecutionInterrupted`
gates on `legalRunEvent(state.Status, ...)` first, and `legalRunEvents` had no entry at all for
`RunCancelledS` — so the very interruption event cancellation exists to produce was rejected as an
illegal transition, every time, unconditionally. Cancelling a run made it structurally impossible to
ever record that its own in-flight node had been interrupted; `kairos cancel` would have hung
silently (the daemon logs the append failure but the client's `Cancel` call had already returned
success). Fixed in `internal/domain/transitions.go` by adding a `RunCancelledS` entry legal for
`NodeExecutionInterrupted` **only** — deliberately not extended to `NodeOutputReceived`/
`NodeExecutionFailed`/`NodeGatesEvaluated`, whose handlers route via graph edges and would resume
forward progress on a run that was just cancelled; `NodeExecutionInterrupted`'s own handler is a
pure status-set with no `Cmd`s, so allowing it cannot reopen that door. Confirmed as a real bug, not
a theorized one, the same way the Fork/context bug in section 3 was: reverting the one-line
`transitions.go` addition reproduces the illegal-transition error deterministically against the same
test.

### The dialogs

Cancel, fork, and source-pause each get a real `<dialog>`, opened only by an explicit button click
(never on page load, never by a single keypress), whose submit button renders `disabled` and is
enabled only once a "type the id to confirm" field matches the dialog's own target exactly — the
same "no accidental single-click destructive action" shape the decision page already established.
Unlike the decision page's typed-word check, cancel/fork/source-pause have no engine-level
typed-confirm concept of their own (neither the CLI nor the TUI's own y/n prompt for `x`/`f`/`Q`
requires one), so `internal/web/mutations.go`'s `requireTypedConfirm` is new, genuine, web-mutation-
layer enforcement: every one of the three routes independently re-checks the posted `confirm` field
server-side and rejects a mismatch with 422 before ever calling the daemon — disabling the page's
JS, or editing the DOM, cannot bypass it (`TestCancelDialog_bypassWithoutMatchingConfirmIsRejected`
and its fork/source-pause siblings in `internal/web/mutations_dialogs_test.go` post exactly such a
bypass and assert the daemon's own call count stays zero). `say` — injecting a message into a
**live** session mid-execution, distinct from conversation send (already built and wired) — has no
daemon capability anywhere in this tree: no session-injection channel to a running child process
exists at any layer. Building one would be new engine/actor infrastructure, not a web dialog over an
existing capability, so per AGENTS.md §7 it stays out of scope here and is named honestly rather than
faked; `resume` (the sources page's other named-but-undialoged verb) is likewise still open.

### The command palette and full keyboard model

`internal/web/static/app.js` gained a literal port of `internal/tui/palette.go`'s `resolvePalette`,
`screenNames`, and `paletteVerbs` — same keys, same "screen name, then verb, then 26-character
Crockford-base32 ULID shape, in that order, no fuzzy fallback" resolution order, no history (input
always starts empty on open, nothing persisted across opens) — `09-cli-and-tui.md`'s explicit design
constraint, ported rather than reinterpreted. `bench`/`benchmark` are deliberately absent (no web
benchmark page exists); `run`/`conversation`/`conversations`/`logs` resolve against the current
page's run context (`<body data-run-id>`, rendered by `renderPage`'s new optional trailing `runID`
argument) exactly as the TUI's own `'l'`/`'c'` bindings only act "if `m.runInspector.runID != ""`" —
with none, the palette reports "no active run in this page" rather than guessing one.

The keyboard model mirrors `internal/tui/keys.go`'s global NAV-mode switch and the per-screen
`j`/`k`/`gg`/`G`/`Enter` list bindings (`screens_home.go`), adapted to page navigation: `h` → home,
`r` → runs, `a` → home (there is no separate web inbox page — "waiting on you" lives on home),
`l`/`c` → the current run's page/conversation (conditional on run context, same as the palette),
`':'`/Ctrl+K/Cmd+K → the palette, `j`/`k` move a highlighted cursor over `[data-nav-list]
[data-nav-item]` rows (home's running table and waiting list, the runs table), `gg`/`G` jump to the
first/last row, `Enter` navigates the highlighted row's own link, `i` focuses the composer, `Escape`
closes a dialog or goes back. Deliberately **not** bound: `q`/`Q` — the TUI quits its own process;
`window.close()` only works on a script-opened window, so a browser tab has no equivalent action a
page can safely take, named here rather than faked with a navigate-away.

**Testing note, consistent with this package's own established practice**: `internal/web`'s
existing tests (the decision screen's `IntersectionObserver` logic) never execute `app.js` — no
browser or JS runtime is an approved dependency in this tree (AGENTS.md's closed dependency table),
and adding one for this pass alone would be exactly the kind of unapproved dependency §1 forbids.
The new tests follow the same shape: `internal/web/palette_keyboard_test.go` asserts the
server-rendered contract every page must carry for the (untestable-by-Go) JS to have anything to act
on — the palette overlay's DOM hooks, `<body data-run-id>` present only on pages with a real run
context, `data-nav-list`/`data-nav-item data-href="..."` rows pointing at the correct URL — and
separately asserts `app.js`'s own source literally contains the TUI's exact screen/verb keys and the
ULID regex, and does **not** contain `localStorage`/`sessionStorage`/a fuzzy-matching call — a
source-fidelity check standing in for execution, the same honest trade-off this package already made
for the decision screen.

## Tests

- `internal/workspace/diff.go`: covered indirectly through `internal/engine`'s tests (it is two
  thin git-invocation wrappers; `internal/engine/diff_test.go` exercises real patches through it).
- `internal/engine/diff_test.go`: a real git fixture (two workspace: write shell nodes, the second
  declaring `workspacePaths` that does **not** cover the file it writes — a real, provoked scope
  violation, not an untested code path), against a real daemon-less `Engine` + real `git`:
  whole-run diff against `BaseRef`, one node's diff seeing only its own change (not the other
  node's), the scope-violation banner naming the right file, and `ErrNoWorkspaceSnapshot` for a
  node that never wrote.
- `internal/engine/fork_context_test.go`: the caller-context-cancellation regression above.
- `internal/web/diff_test.go`: a real `git diff --unified=3` patch (hand-captured, not invented)
  proves chroma highlighting actually renders (`chroma` class present), the scope banner renders
  from real `ScopeViolations`, both render modes produce the right markup, and a `?file=&line=`
  deep link redirects to a real anchor that exists on the page.
- `internal/web/compare_test.go`: real cost/duration/attempts/findings rendering (duration
  formatted via the existing `dur` template func — `4m12s`/`6m48s`, matching 10-webui.md's own
  mockup numbers), the fork-drift annotation, and that a compare page **never** renders a `$` cost
  figure (NL-48's honesty preserved at the render layer, not just the API).
- `cmd/kairos/diff_compare_webui_test.go`: the real end-to-end proof — a real git source repo, a
  real daemon (`KAIROS_WORKSPACE_REPO`/`KAIROS_BASE_REF`), a real two-node workspace: write run, the
  real web diff viewer (whole-run and per-node, confirming the scope banner against genuine daemon
  output), a real fork forced to drift (`--at <run.started's own sequence> --allow-drift`), and the
  real compare page confirming the fork relationship and drift detail — end to end, no daemon
  mocked.
- `internal/web/newpages_test.go`: the fake-daemon unit tests for all seven operations pages —
  the events explorer (unfiltered rows, and a `?type=` filter narrowing the table while the causal
  tree for a `?focus=` event still resolves its real ancestor/root despite that filter), findings
  (`state=failed` default vs `state=all`), gates (fires/failures/catch-rate/waivers computed from a
  real mixed pass/fail/waiver fixture), cost (estimate + cap rendered, the NL-30 honesty note
  present), sources (health/consecutive-errors/cursor rendered verbatim), runners (the one honest
  `local` row, and a negative assertion that no invented second runner ever appears), and flows
  (grouped by `DefinitionRef` from a real multi-run fixture).
- `cmd/kairos/webui_operations_test.go`: the real end-to-end proof for all seven — a real daemon, a
  real one-node/one-gate workflow run to `succeeded` (a genuine `constraint.evaluated` and
  `trigger.received` event, not fixtures), a real registered source
  (`kairos src add gh-issues --kind poll`), then all seven pages fetched through the real one-time-
  token session and asserted against that real data: the events page's causal tree resolving a real
  `?focus=<seq>`, findings/gates showing the real `always-passes` gate, cost showing its honesty
  note, sources showing the real registered row, runners showing `local`, and flows showing the
  real definition path — closed with a real `db verify` showing no mismatches.
- `internal/engine/cancel_test.go`: `--reason` required; rejects an already-terminal run; rejects a
  `Pending` run (never legal per `legalRunEvents`); and the real end-to-end proof — a genuinely
  executing shell node, cancelled mid-flight, reaching `RunCancelledS` with its node recorded
  `node.execution.interrupted` — the test that found the `RunCancelledS`/`legalRunEvents` bug above.
- `internal/domain`: the existing suite re-run under `-race` after the `transitions.go` fix (no new
  domain test file — the fix is exercised by `internal/engine/cancel_test.go`'s own end-to-end case,
  which folds through the real `domain.Advance`, not a mock).
- `internal/api/cancel_test.go`: `POST /runs/{id}/cancel` with no engine configured → 503, a
  malformed body → 400 (mirroring `TestHandleApprove_noEngineConfiguredIs503`'s existing shape); the
  reason-required/already-terminal/not-cancellable → 422/409 mapping is exercised through the real
  engine by `internal/engine/cancel_test.go` and end to end by `cmd/kairos/cancel_cli_test.go`.
- `internal/web/mutations_dialogs_test.go`: cancel/fork/source-pause each get a bypass test (missing
  or mismatched `confirm` field → 422, and the daemon call count stays exactly zero) and a matching-
  confirm positive-path test (the exact field the dialog's own JS would allow through reaches
  `deps.Client.Cancel`/`Fork`/`PauseSource` with the right arguments); the run page's dialogs render
  closed and `disabled` by default; the sources page renders a pause dialog only for a still-enabled
  source.
- `internal/web/palette_keyboard_test.go`: `app.js`'s palette contains every `internal/tui/
  palette.go` screen/verb key and the ULID shape check, and none of `bench`/`benchmark`/history/
  fuzzy-matching; the keyboard model contains every TUI global-key binding and excludes `q`/`Q`;
  every page renders the palette overlay + keyboard-status DOM hooks; `<body data-run-id>` is
  present with the real id on run/decision/conversation/diff pages and empty on home; home/runs
  render `data-nav-list`/`data-nav-item data-href="..."` rows pointing at the correct URL.
- `internal/cli/cancel_test.go`: `kairos cancel` has `--reason`, never `--yes`/`--all`/`--compensate`/
  `-f`, and a missing `--reason` exits 2 before ever touching a daemon.
- `cmd/kairos/cancel_cli_test.go`: the real end-to-end proof — a real daemon, a genuinely sleeping
  shell node cancelled mid-flight via the real built binary, the run reaching `"cancelled"`, its
  event log carrying both `run.cancelled` and `node.execution.interrupted`, a second cancel attempt
  rejected, and a closing `db verify` showing no mismatches.

## Verification

- `go build ./...`, `go build -tags dev ./...`, `go vet ./...`, `gofmt -l .` — clean.
- `golangci-lint run` — clean (two `staticcheck` findings fixed, not suppressed: a tagged-switch
  suggestion in `engine/diff.go`, an unused-return-value case guard in `web/diffparse.go`; the
  cancel/palette/keyboard pass added no new findings).
- `go test ./... -race` — full suite green, including every new test above under `-race`.
- `make arch` — clean, including the web-route-parity checks, now extended by this pass's three new
  `WebPath`s (`GET /events`, `GET /sources`, `GET /cost` from the operations-pages pass;
  `POST /runs/{id}/fork`, `POST /runs/{id}/cancel`, `POST /sources/{id}/pause` from this pass).
- `make cross` (darwin/linux × amd64/arm64, `CGO_ENABLED=0`) — clean.

## New limitations registered

- **NL-51** — the diff viewer's side-by-side pairing is position-based, not content-based (a
  reordered block within a hunk can misalign a row); the unified view is unaffected and one click
  away.
- **NL-52** — the diff viewer's syntax highlighting is one fixed dark theme; it does not repaint
  under light mode the way the rest of the page's CSS tokens do.

Both are cosmetic, both have a named, scoped revisit path, and neither weakens anything this pass's
own tests assert. No new limitation came out of the operations pages pass: the Cost/Flows/Runners
pages' narrower scope is each an *existing* limitation (NL-30, the absent definitions registry, the
absent runner registry) surfaced honestly at the render layer, not a new gap this pass introduced.

No new limitation came out of the cancel/palette/keyboard pass either: the one real gap it found
(`RunCancelledS` missing from `legalRunEvents`) is a bug, fixed in this pass, not a standing
limitation to register; `say`'s absence is scope discipline (no session-injection capability exists
anywhere to build a dialog over), named in Future work below, not a limitation of what was built.

## Future work

Named honestly, not silently dropped:

- **Diff-of-diffs** on the compare page (`10-webui.md`'s "both changed / only in B" file list) —
  this pass built the cost/duration/attempts/findings/drift side-by-side the task asked for; the
  file-level diff-of-two-diffs view is a natural next consumer of `Engine.Diff`, not built here.
- **CoW-restore wiring into the diff/fork paths** (NL-46/NL-47 in `L18-fork-replay-verify.md`) — a
  diff viewer reading only the git-ref layer is unaffected by gitignored build state; still open.
- **`⤓ ndjson`/CSV export** on the events/findings/runs pages (`10-webui.md`'s own mockups) — not
  built this pass on either page added here.
- **Sources resume/poll-now dialogs** on the web `/sources` page — this pass built the pause
  dialog only (the item L20-webui.md's Future work actually named); `kairos src resume` has a real
  daemon route and is unaffected but has no web dialog yet. `POST /sources/{id}/poll-now` itself
  does not exist anywhere yet (CLI included) — a genuinely new capability, not merely a missing web
  dialog, so it stays out of scope here too.
- **Gates/findings grouped by file**, matching `10-webui.md`'s exact framing ("grouped by
  constraint and by file") — this pass groups by gate/constraint only; per-file grouping is a
  straightforward addition to the same computed data, not built here.
- **`kairos say` / injecting a message into a live session** — genuinely absent at every layer (no
  daemon route, no CLI verb, no session-injection channel to a running child process anywhere in
  `internal/engine`). L20-webui.md's Future work item asserted this already existed; it did not.
  Building it is new engine/actor infrastructure, not a web dialog over an existing capability —
  out of scope for this document per AGENTS.md §7, named honestly rather than invented.
- **Dark-mode contrast CI, the broader accessibility pass, packaging** — unrelated to this pass,
  unchanged from L20-webui.md's original Future work.

## 6. The visual revamp — a real design system, not a token gesture

The fourth pass through this log, and the one that closes the "web UI buildout + revamp" arc: every
page from L20 through section 5 above was functionally complete but visually a bare `html/template`
default (system-ui body font, browser-default `<button>`s, a handful of scattered hex colors). This
pass restyled the whole app in place — `internal/web/static/app.css` rewritten end to end, three
templates (`home.gohtml`, `runs.gohtml`, `run.gohtml`) given a handful of additive class hooks — with
**zero changes to `app.js`, route handlers, or any Go rendering logic**. ADR 0007's constraint held
throughout: still `html/template` + `//go:embed`, still one hand-written CSS file, no Node, no
bundler, no build step. `go build ./...` still produces the whole thing.

### Dark-mode-first, not a toggle — and why

The page commits to a genuine dark palette as its authored default and answers `prefers-color-scheme`
honestly for a system set to light; it does **not** grow a manual light/dark switch. Two reasons, both
already load-bearing elsewhere in this design: first, the TUI (bubbletea/lipgloss) is dark by
convention with no light mode of its own, and the CLI's output has no concept of "theme" at all — a
toggle would make the web surface the only one of the three with a choice to make, working against
"CLI/TUI/web should feel like one coherent product." Second, and more structurally: a manual toggle is
client-held UI state, and ADR 0007's central rule for this whole surface is **"no surface holds
state"** — both the web UI and the TUI are stateless clients of the same API, precisely so an SSE event
can always trigger a server re-render rather than a client-side patch. A `data-theme` cookie or
`localStorage` preference would be the first exception to that rule, for a preference the browser
already reports for free. `prefers-color-scheme` is not a compromise here; it is the more honest
answer available.

### Typography

One font stack, used everywhere, headings included: `ui-monospace, "SF Mono", "Cascadia Code",
"JetBrains Mono", Menlo, Consolas, "Liberation Mono", monospace`. No web font is loaded — ADR 0007's
"the page works with no network at all" extends to typography the same way it already applies to htmx
being vendored rather than CDN-linked; every name in that stack is a system font already on the
platforms this ships for. Committing to monospace for body copy as well as data (not just diffs and
`<code>`, which already had it) is the one explicitly requested identity choice: a run id, a gate name,
and a sentence of prose now render in the same typeface a `kairos show` or a TUI screen would use,
so screenshots of the three surfaces side by side read as one product's outputs, not three.

### Color, spacing, elevation — token-based, not scattered

Every color, spacing, radius, shadow, and motion-duration value used anywhere in the stylesheet is a
CSS custom property declared once in `:root` (dark) and re-declared once under
`@media (prefers-color-scheme: light)` — no component rule hand-writes a hex code or a raw `rem`
value. The scale:

- **Spacing**: `--space-1` through `--space-8`, a 4px-based scale (0.25rem…3rem), used for every
  margin/padding/gap in the file.
- **Color**: a semantic set, not a swatch — `--bg`/`--bg-elevated`/`--bg-inset` (three depths, used for
  page background / card background / code-and-diff background respectively, the closest thing this
  flat, cardless design gets to elevation), `--fg`/`--fg-muted`/`--fg-subtle` (three text emphasis
  levels), `--border`/`--border-strong`, `--accent`/`--accent-strong`, and `--risk`/`--pass`/`--warn`
  each with a paired `-bg` wash for status pills and banners. Components reference the semantic name
  (`var(--risk-bg)`), never the palette directly, so the light override is a complete second palette
  behind the same names rather than a per-component light/dark branch.
- **Elevation**: no drop shadows standing in for a card system that doesn't otherwise exist in this
  flat design — `--shadow-sm/md/lg` are used sparingly, for the two floating surfaces that genuinely
  lift off the page (the command palette and the confirm dialogs), plus a faint `--shadow-sm` on
  `.pane`. Depth otherwise comes from the three background tokens, not from shadows layered on a flat
  color.
- **Motion**: `--dur-fast` (110ms, hover/focus state changes) and `--dur-med` (180ms, the dialog/palette
  entrance), one easing curve (`--ease`), and a single `@media (prefers-reduced-motion: reduce)` block
  that collapses every animation/transition duration to near-zero — checked by hand against Chrome
  headless with `prefers-reduced-motion` emulated, not merely asserted in a comment.

### Componentry added, all in plain CSS

Real button styling (dark tools ship real buttons, not the browser default grey rectangle — a filled
accent style for `type="submit"`, an outlined style otherwise, a pressed-state `translateY` on
`:active`), status pills (`.pill`, `.pill-running`/`.pill-succeeded`/`.pill-failed`/etc., driven by the
existing `{{.Status}}` string already present in `home.gohtml`/`runs.gohtml`/`run.gohtml` — the class
name is templated as `pill-{{.Status}}`, no new Go data), a consistent `.note`/`.error`/`.empty` state
family, a redesigned sticky topbar (backdrop-blur, a small diamond mark before the wordmark), and a
genuine dialog/palette entrance animation (`@keyframes dialog-in`, respecting reduced-motion). Every
interactive element gets a real `:focus-visible` ring (`--focus-ring`, a `box-shadow` rather than the
browser default outline, chosen so it reads consistently across `<button>`, `<a>`, `<input>`, and the
`[data-nav-item].nav-cursor` rows the keyboard model already drives) — this was a gap in the prior CSS
(no `:focus-visible` rule existed at all outside the nav-cursor outline) and is a genuine accessibility
improvement, not merely cosmetic.

### A real, pre-existing bug this pass's own screenshot check found

Rendering the page through headless Chrome (`google-chrome --headless=new --screenshot`, real
Chrome 151 — no Node/npm/bundler pulled in to get it, and nothing in this repo depends on it existing;
it was used once, by hand, as this pass's screenshot-equivalent check, exactly as
`L23-webui-revamp.md`'s own task framing invited) surfaced a genuine, pre-existing rendering bug in
`.palette-overlay`, present since the command-palette pass (section 5) and **not introduced by this
one**: the rule set `display: flex` unconditionally, and CSS cascade origin rules mean an author-origin
declaration always wins over the user-agent stylesheet's `[hidden] { display: none }` **regardless of
selector specificity** — so toggling the palette's `hidden` IDL property from `app.js` was silently
not hiding it. The command palette rendered open, and visible, on every page load. Confirmed visually
(a screenshot with the fix reverted shows the overlay covering the whole page on initial load) and
fixed with one additive rule, `.palette-overlay[hidden] { display: none; }`, scoped to the same
selector so it changes nothing else. `TestLayout_rendersPaletteOverlayAndKeyboardStatusHooks` already
asserted the *server-rendered* `hidden` attribute was present in the markup — the right test for what
Go controls — but nothing in this tree executes CSS cascade rules, so a real browser render was the
only way this was ever going to surface. No existing test covered client-rendered visibility (JS/CSS
execution is untestable-by-Go in this stack, the same honest limitation section 5's own tests already
name), so no test regressed; this is a real bug found and fixed by the screenshot check the task asked
for, not a theorized one.

### What stayed exactly as it was

`app.js` is untouched — every behavioral invariant it enforces (the decision screen's
`IntersectionObserver`-gated fieldset, the confirm-dialog typed-match enablement, the command
palette's screen/verb/ULID resolution order, the keyboard model's global bindings) is driven by DOM
hooks and JS logic this pass never touched. The three template edits made (`home.gohtml`/
`runs.gohtml`/`run.gohtml`, wrapping existing status strings in a `.pill` span and existing tables in a
`.table-wrap` scroll container) are additive: every id, `data-*` attribute, and DOM-order constraint
the existing test suite depends on — `data-pane="risk"` before `data-pane="findings"` before
`data-pane="decision"`, the fieldset's `disabled` attribute inside its own tag, `data-confirm-submit
disabled` appearing exactly twice on the run page, `id="cancel-dialog"`/`id="fork-dialog"` present and
never rendering with an `open` attribute, `id="pause-dialog-{id}"` only for an enabled source, the
literal `palette-overlay" class="palette-overlay" hidden` string, `data-run-id`/`data-nav-list`/
`data-nav-item data-href="..."` — is byte-identical to before. `go test ./... -race`, `golangci-lint
run`, `gofmt -l .`, `make arch`, and `make cross` are all clean after this pass with no test edited.

This closes the "web UI buildout + revamp" arc L20 opened and L23 has carried since: every page named
in 10-webui.md's original mockups now exists, is wired to a real daemon end to end, and looks like a
considered product rather than an unstyled placeholder for one.
