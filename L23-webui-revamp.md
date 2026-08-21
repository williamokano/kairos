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

## Verification

- `go build ./...`, `go build -tags dev ./...`, `go vet ./...`, `gofmt -l .` — clean.
- `golangci-lint run` — clean (two `staticcheck` findings fixed, not suppressed: a tagged-switch
  suggestion in `engine/diff.go`, an unused-return-value case guard in `web/diffparse.go`).
- `go test ./... -race` — full suite green, including every new test above under `-race`.
- `make arch` — clean, including the two web-route-parity checks extended by this pass's three new
  `WebPath`s (`GET /events`, `GET /sources`, `GET /cost`).
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

## Future work

Named honestly, not silently dropped:

- **Diff-of-diffs** on the compare page (`10-webui.md`'s "both changed / only in B" file list) —
  this pass built the cost/duration/attempts/findings/drift side-by-side the task asked for; the
  file-level diff-of-two-diffs view is a natural next consumer of `Engine.Diff`, not built here.
- **CoW-restore wiring into the diff/fork paths** (NL-46/NL-47 in `L18-fork-replay-verify.md`) — a
  diff viewer reading only the git-ref layer is unaffected by gitignored build state; still open.
- **`⤓ ndjson`/CSV export** on the events/findings/runs pages (`10-webui.md`'s own mockups) — not
  built this pass on either page added here.
- **Sources pause/resume/poll-now dialogs** on the web `/sources` page — the CLI verbs
  (`kairos src pause|resume`) exist and are unaffected; this pass's `/sources` is read-only.
  `POST /sources/{id}/poll-now` itself does not exist anywhere yet (CLI included) — a genuinely new
  capability, not merely a missing web dialog, so it stays out of scope here too.
- **Gates/findings grouped by file**, matching `10-webui.md`'s exact framing ("grouped by
  constraint and by file") — this pass groups by gate/constraint only; per-file grouping is a
  straightforward addition to the same computed data, not built here.
- **Command palette (⌘K) and full keyboard model, dark-mode contrast CI, packaging** — unrelated to
  this pass, unchanged from L20-webui.md's original Future work.
