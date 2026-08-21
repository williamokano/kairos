# L23 — web UI revamp

Not a numbered build document (the plan ends at L20; L21/L22 are running hardening/integration
logs) — a running log, same shape as L21/L22, picking up the first two items named in
L20-webui.md's own Future work: the diff viewer and the compare page. Both were named there as
"the web rendering is missing" — `internal/cli.Client.Compare`/L18's `Engine.Compare` already
existed for compare; the diff viewer needed real daemon-side machinery too (nothing had ever
exposed a node's or a run's file-level change before this pass — `decision.gohtml`'s own CHANGED
pane said so plainly: "no diff data exposed by the daemon API yet").

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

## Verification

- `go build ./...`, `go build -tags dev ./...`, `go vet ./...`, `gofmt -l .` — clean.
- `golangci-lint run` — clean (two `staticcheck` findings fixed, not suppressed: a tagged-switch
  suggestion in `engine/diff.go`, an unused-return-value case guard in `web/diffparse.go`).
- `go test ./... -race` — full suite green, including every new test above under `-race`.
- `make arch` — clean.
- `make cross` (darwin/linux × amd64/arm64, `CGO_ENABLED=0`) — clean.

## New limitations registered

- **NL-51** — the diff viewer's side-by-side pairing is position-based, not content-based (a
  reordered block within a hunk can misalign a row); the unified view is unaffected and one click
  away.
- **NL-52** — the diff viewer's syntax highlighting is one fixed dark theme; it does not repaint
  under light mode the way the rest of the page's CSS tokens do.

Both are cosmetic, both have a named, scoped revisit path, and neither weakens anything this pass's
own tests assert.

## Future work

Named honestly, not silently dropped — the remainder of L20-webui.md's own Future work list that
this pass did not touch:

- **Diff-of-diffs** on the compare page (`10-webui.md`'s "both changed / only in B" file list) —
  this pass built the cost/duration/attempts/findings/drift side-by-side the task asked for; the
  file-level diff-of-two-diffs view is a natural next consumer of `Engine.Diff`, not built here.
- **CoW-restore wiring into the diff/fork paths** (NL-46/NL-47 in `L18-fork-replay-verify.md`) — a
  diff viewer reading only the git-ref layer is unaffected by gitignored build state; still open.
- Everything else L20-webui.md's Future work already named (event log explorer, findings/cost/
  gates/sources/runners/flows pages, command palette, dark-mode contrast CI, packaging) — unrelated
  to this pass, unchanged.
