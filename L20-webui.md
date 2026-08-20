# L20 — The web UI

This is the final document in the 21-document build plan (L00 through L20, L07/L09 renumbered
early, L15/L19/L20 renumbered late — the same "twenty-one documents, renumbered" sequence
`12-build-plan.md` describes throughout). It has no outgoing edges in the build graph.

## Depends on

L14 (conversations) and L19 (self-check + chaos), both committed. Transitively everything else —
this document is the daemon's third client surface, touching almost every prior layer's existing
API rather than adding new daemon capability.

## Scope

`10-webui.md` specifies ~50 developer-days across fifteen-plus pages, a command palette, full
keyboard parity, dark mode/a11y, and packaging. **This pass does not build all of it — doing so
honestly in one sitting is not possible, and claiming otherwise would be the exact kind of
padding this project's discipline exists to prevent.** Per the doc's own staged-rollout
guidance ("Read-only core... The two things the terminal cannot do... Do not stop between 2 and 3
for long"), this pass builds stage 1 (read-only core) plus the decision page from stage 2 — the
highest-priority, safety-critical page — plus enough of stage 3 (composer, conversation, answer)
to make the surface genuinely actionable rather than a pure viewer. Everything else is named
explicitly in Future work below, not silently dropped.

**In.**
- `internal/web`: a new package, the daemon's third API client (after `internal/cli` and
  `internal/tui`) — never imports `internal/api`, talks only through `internal/cli.Client` over
  the same unix socket, exactly matching ADR 0008's "terminal is a client" reasoning extended to
  the browser.
- Real htmx (vendored as a checked-in file, fetched once and committed — not a CDN reference) plus
  its SSE extension, per ADR 0007.
- The full auth surface from `10-webui.md`'s table: loopback-only bind refusing a non-loopback
  address without an explicit acknowledgement string, a per-`kairos serve` random 32-byte bearer
  token at `$KAIROS_HOME/web-token` (mode 0600), one-time `?t=` query-to-cookie exchange stripping
  the token from the URL, `Host` header allowlist, `Origin`/`Sec-Fetch-Site` checks on every
  mutating request, and a strict CSP.
- Pages: home (composer + waiting-on-you + running), run list, run detail (node table + live
  event tail via SSE), the decision/approval page (full anti-rubber-stamp parity with L15's TUI
  screen), conversation (with a composer), doctor.
- Mutations: start a run, answer a decision, post a conversation message — each reaching the exact
  same daemon-side validation the CLI and TUI already use, never a parallel or weaker path.
- `internal/decisionctx` (new, extracted from `internal/tui`): the decision-evidence computation
  L15 built, now shared verbatim between the TUI and the web UI so the two surfaces cannot
  silently diverge on what "risk" means — `10-webui.md`'s own stated requirement ("the decision
  card is identical in both, rendered from the same server-computed context").
- `kairos web`: mints the one-time-token URL and opens a browser, mirroring `serve`/`tui`'s
  existing `ServeFunc`/`TUIFunc` injection pattern from `cmd/kairos` (a new `WebFunc`).
- The agent-facing socket (`agent.sock`), real for the first time — `TestArchitecture_agentSocketRouteSubset`
  was named and stubbed since L04, waiting on actor invocation (L08, done) and a document that
  actually needed the boundary enforced. It carries exactly one route (`GET /status`) today; see
  Documented decisions #9.
- `apispec.Op` gains an optional `WebPath` field and a new architecture test,
  `TestUI_webRoutesResolve`, extending `TestUI_everyCallHasCLICounterpart`'s parity discipline to
  the third surface.

**Out** (named here, not silently dropped — see Future work for the full list): the diff viewer
(chroma syntax highlighting, side-by-side/unified, scope-violation banner); the fork-compare
diff-of-diffs page; the event log explorer with a causal tree; findings/cost/gates/sources/runners/flows
pages; the command palette (⌘K) and full keyboard model; dark-mode contrast CI and the broader a11y
pass; CSV export; packaging (Homebrew formula, launchd/systemd units); cancel/fork/say/source-pause
web dialogs (the CLI/TUI paths for all of these already exist and are unaffected).

## Documented decisions

1. **Content composed by rendering into a buffer, then wrapping in `"layout"`** — not Go's
   `{{block}}`-override idiom. `html/template.ParseFS` associates every file matched by one glob
   into a single shared template namespace, so N pages each defining `{{define "content"}}` would
   silently collide (the last one parsed wins, globally) rather than coexisting. `renderPage`
   executes the named page template to a `bytes.Buffer` first, then executes `"layout"` with that
   buffer as `template.HTML`. A deliberate, documented departure from the idiom shown in some Go
   template tutorials, not an oversight.
2. **The decision page renders synchronously — fetch evidence, then render once** — not the
   mockup's implied progressively-hydrated per-pane loading. A server-rendered document that
   already has every fact in hand at request time has no meaningful "still loading" state to
   model; `10-webui.md`'s "page renders its fragments inline on first paint" already says this is
   correct for every page, the decision page included. "Failed evidence blocks the form" is
   therefore implemented as: a fetch failure renders the blocked state directly, first paint —
   the same rule, expressed for a synchronous render instead of an async one.
3. **Anti-rubber-stamp focus-order enforcement is `IntersectionObserver`-based dwell tracking, not
   a `Tab`-walk** — the TUI's screen-based navigation has a natural notion of "which pane has
   focus"; a fully-rendered web document showing every pane at once does not. `app.js` marks a
   pane "viewed" once it has been scrolled into view, and enables the decision `fieldset` only
   once every pane is viewed and every high/critical finding's risk checkbox is checked — this is
   `10-webui.md`'s own stated mechanism ("the one thing the browser does better:
   `IntersectionObserver` gives exact per-pane visible time"), not an invention. It is explicitly
   a client-side UX aid: **the server independently re-validates** at `POST /runs/{id}/approve` —
   the same handler L13 built, completely unmodified by this document — regardless of what the
   client allowed through. Verified directly: `TestDecisionAnswer_reachesTheSameServerValidationAsApprove`
   posts a form the client-side JS would never have enabled and confirms the server's own rejection
   (not a client-side one) is what surfaces.
4. **The typed-confirm field is always rendered and always asks for the node ID** — matching
   `internal/engine`'s actual server-side check (`ans.TypedWord != nodeID`, gated on
   `wait.weight == "type"`, from L13's `human.go`) rather than `10-webui.md`'s more general "type
   the decision to confirm" mockup text. The TUI (L15) already has this same behavior — it always
   submits the node ID as `TypedWord` regardless of what's displayed — so the web form matches the
   TUI's real behavior, not the doc's more idealized prose, keeping the two surfaces genuinely
   identical rather than each independently guessing at the doc's intent.
5. **"Waiting on you" scans each active run's `Executions` for a `waiting` node** — `10-webui.md`
   names a `GET /human-tasks?state=open` endpoint that does not exist in the daemon API. Building
   a real index endpoint is out of this pass's scope (a daemon-side change touching
   `internal/eventstore`'s projections, not a web-UI-layer concern); the home page instead does an
   O(active runs) fetch per load. Named honestly here and in Future work, not hidden behind a
   plausible-looking but fake endpoint.
6. **The `Idempotency-Key`/form-nonce is minted and rendered but not yet enforced server-side** —
   `10-webui.md`'s composer example expects the daemon to dedupe a double-submit by this header;
   `internal/api`'s `POST /runs` does not implement that dedupe yet (a real, pre-existing gap this
   document did not introduce and does not fix, since fixing it is a daemon/eventstore change
   outside a UI-layer document's scope). The nonce is rendered so the wiring is ready for whichever
   document adds the dedupe; it is inert today. Named as NL-49 below.
7. **The SSE proxy dials the admin unix socket directly via a custom `httputil.ReverseProxy`
   transport**, bypassing `internal/cli.Client.Events` (a bounded, buffering historical read) —
   the live tail needs the daemon's raw chunked response, `Last-Event-ID` semantics, and flush
   timing intact, which only a genuine proxy preserves. This is the one place `internal/web`
   touches the unix socket below `cli.Client`'s abstraction, and it is documented exactly why:
   reusing the daemon's existing resumption guarantee (ADR 0010) rather than reimplementing it.
8. **`internal/decisionctx` is a new shared package**, not a duplication of L15's
   `buildDecisionContext` into `internal/web`. Duplicating safety-critical risk-computation logic
   across two UI surfaces is exactly the kind of drift `10-webui.md` calls out by name ("the
   decision card is identical in both... Presentation may differ; content may not"). `internal/tui`
   was refactored to consume the shared package (a real, tested, minimal-diff extraction — its own
   test suite stayed green throughout) rather than left with a parallel copy.
9. **The agent-facing socket's honest current content is one route: `GET /status`.** No document
   through L19 built an agent-initiated HTTP callback into the daemon — `check-output` (L08) reads
   `$KAIROS_OUTPUT`/`$KAIROS_SCHEMA` from the local filesystem with no daemon round trip at all
   (see `checkoutput.go`'s existing doc comment), and `artifact stage`/`ask-human` are named in
   `10-webui.md`'s auth section but have no implementation anywhere in this codebase. Building a
   full RPC surface for agents that call nothing today would be speculative infrastructure AGENTS
   §7 warns against. What matters — and what is real, not aspirational — is the **absence**:
   `TestArchitecture_agentSocketRouteSubset` asserts every admin-only mutating route 404s on this
   socket while resolving normally on the admin one, so the boundary exists and is enforced the
   moment a future document needs to add a real agent-facing route to it.
10. **Static assets vendored via a one-time network fetch during this session, not hand-authored.**
    ADR 0007 requires htmx be a checked-in file, never a CDN reference at runtime; it does not
    require it be typed by hand. `htmx.min.js`/`htmx-ext-sse.js` were fetched once from their
    canonical release URLs and committed as ordinary vendored files — the same operational meaning
    as any other vendored dependency in this repository, verified working end-to-end against a
    real daemon (auth flow, page rendering, form submission) before committing.
11. **Browser-opening (`kairos web`) is honestly untested by automation.** `cmd/kairos/web.go`
    shells out to `open`/`xdg-open`/`start` by OS; no CI environment here has a real display or
    browser to verify the launch actually opens a window, only that the daemon becomes ready and
    the token URL is printed/computed correctly (which is tested). A failed `exec.Command.Start`
    degrades to printing an error and still returning success — the verb's primary promise (daemon
    up, correct URL) held even if the convenience launch fails.

## Public interfaces

```go
// internal/web
type Deps struct {
	Client       *cli.Client
	SockPath     string
	Token        string
	AllowedHosts []string
}
func NewMux(deps Deps) http.Handler
func NewRawMux(deps Deps) *http.ServeMux // unwrapped, for introspection/testing
func GenerateToken() (string, error)
func Listen(addr, ack string) (net.Listener, error)
const RequiredNonLoopbackAck = "yes-lan-only-behind-a-firewall"

// internal/decisionctx
type Context struct { Effect string; Irreversible bool; Gates []GateVerdict; Findings []FindingSummary; Attempts int }
func Build(envs []cli.Envelope, nodeID string) Context
func (c Context) HighOrCritical() []FindingSummary

// internal/api (agent socket)
func NewAgentMux(deps Deps) *http.ServeMux
var AgentSocketForbiddenPatterns []struct{ Method, Path string }

// internal/apispec, extended
type Op struct { Method, Path, CLIVerb, WebPath string }

// internal/cli
type WebFunc func(ctx context.Context, sockPath, homePath string) error
func Execute(args []string, starter DaemonStarter, serve ServeFunc, tui TUIFunc, web WebFunc) int
```

## Files to create

```
internal/web/server.go  render.go  embed.go  embed_dev.go  pages.go  frag.go  mutations.go
internal/web/templates/layout.gohtml  home.gohtml  runs.gohtml  run.gohtml  decision.gohtml
internal/web/templates/conversation.gohtml  doctor.gohtml
internal/web/templates/frag/timeline.gohtml  runrow.gohtml  message.gohtml
internal/web/static/htmx.min.js  htmx-ext-sse.js  app.css  app.js
internal/web/auth_test.go  decision_test.go  pages_test.go  testdaemon_test.go

internal/decisionctx/decisionctx.go

internal/api/agentsocket.go

cmd/kairos/web.go  web_test.go
cmd/kairos/testdata/web_approval.yaml

# modified:
internal/tui/decision.go  keys_decision.go  view_decision.go  (decisionctx extraction)
internal/cli/root.go  serve.go
internal/config/config.go
internal/apispec/ops.go
internal/archtest/ui_cli_parity_test.go  deferred_test.go
internal/archtest/web_route_parity_test.go  agent_socket_route_subset_test.go  (new)
cmd/kairos/main.go  serve.go
cmd/kairos/kill_mid_run_test.go  (real bug fix, see Acceptance criteria)
```

## Data changes

None. The web UI reads/writes exclusively through the existing daemon API; no new event types, no
new SQL tables. `$KAIROS_HOME/web-token` and `$KAIROS_HOME/agent.sock` are new files, not database
state — regenerated/rebound every `kairos serve`, matching `daemon.sock`'s existing lifecycle.

## Acceptance criteria

- `go build ./...`, `go build -tags dev ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run`
  clean; `go test ./... -race` green across every package.
- All architecture tests pass, including the newly-real `TestArchitecture_agentSocketRouteSubset`
  and the new `TestUI_webRoutesResolve`.
- `make cross` builds all four platform/arch combinations; `make arch` clean.
- A real end-to-end test (`cmd/kairos/web_test.go`) drives the actual built binary: reads the real
  `web-token`, performs the one-time `?t=` exchange, fetches the home page, runs the milestone's
  human-approval fixture, fetches the decision page (confirming the fieldset renders `disabled`),
  and posts the answer through the web form — confirming the run reaches `succeeded` from a
  browser-shaped request, not a mock.
- 20 unit tests in `internal/web` cover every row of `10-webui.md`'s auth table (unauthenticated
  rejection, bearer acceptance/rejection, one-time-token exchange and cookie semantics, Host
  allowlist, Origin/Sec-Fetch-Site checks, CSP headers, loopback-bind enforcement) plus the
  decision page's evidence-before-controls DOM order, evidence-load-failure blocking, the
  server-side-validation-is-authoritative property, and the structural absence of any bulk/global
  approve route.
- **A real, previously-latent bug found and fixed while writing this document's own tests**: `cmd/kairos`'s
  `daemonHarness.start` (from L05) never registered a `t.Cleanup` to kill the daemon process it
  started. A test that failed between `start()` and its own explicit teardown leaked a live daemon
  holding the web UI's listen port — the *next* test's `start()` then failed with "address already
  in use," a symptom that looked unrelated to its actual cause. Fixed with an unconditional
  `t.Cleanup(func() { syscall.Kill(-pid, SIGKILL) })`, safe even when the daemon is already dead
  (an ESRCH is swallowed). This benefits every test in the package, not just L20's new one.

## Tests

- `internal/web/auth_test.go`: every row of the auth table, both positive and negative.
- `internal/web/decision_test.go`: fieldset-disabled-by-default + evidence-before-controls DOM
  order, evidence-load-failure blocking, server-validation-is-authoritative, no-bulk-approve-route.
- `internal/web/pages_test.go`: home page renders real run data, empty-state messaging, static
  asset serving, conversation post/render round trip.
- `internal/web/testdaemon_test.go`: a hand-rolled fake daemon (never importing `internal/api`,
  preserving the "web is a pure client" boundary even in tests) backing the above.
- `cmd/kairos/web_test.go`: the real-binary end-to-end test described above.
- `internal/archtest/agent_socket_route_subset_test.go`,
  `internal/archtest/web_route_parity_test.go`: the two new/completed architecture tests.

## Benchmarks

None. Loopback HTTP handlers over a local SQLite-backed daemon are not on a durability-sensitive
hot path at a scale that warrants one.

## Migration

None from a prior version.

## Future work

This is the honest remainder of `10-webui.md`'s ~50-day scope, staged per the doc's own ordering:

- **The diff viewer** (chroma syntax highlighting, side-by-side/unified, scope-violation banner,
  deep links) — the doc's own stage-2 priority alongside the decision page; the decision page was
  chosen first here as the safety-critical one.
- **Compare page** (`/compare?a=&b=`, diff-of-diffs) — `internal/cli.Client.Compare`/L18's
  `Engine.Compare` already exist; only the web rendering is missing.
- **Cancel/fork/say/source-pause dialogs** — the CLI verbs and daemon routes all exist
  (`kairos cancel`/`fork`/`say`/`src pause`); this pass's mutation set was scoped to
  start/answer/message only.
- **Event log explorer with a causal tree**, **findings/cost/gates/sources/runners/flows pages** —
  each a filtered-table-plus-detail page per `10-webui.md`'s own description; none built this pass.
- **Command palette (⌘K) and full keyboard model parity with the TUI** — `10-webui.md` budgets
  ~230 of its ~600 JS lines to these two features alone; this pass's `app.js` is ~90 lines,
  covering only the decision screen's anti-rubber-stamp logic.
- **Dark-mode contrast CI, the broader accessibility pass, CSV export.**
- **Packaging**: Homebrew formula, launchd/systemd user units, `kairos up --install`.
- **`GET /human-tasks?state=open`**: a real daemon-side index so the home page's "waiting on you"
  section stops being an O(active runs) scan (Documented decision #5).
- **`Idempotency-Key` server-side dedupe** on `POST /runs` (Documented decision #6) — registered
  as **NL-49** in `11-limitations.md`.
- **The agent-facing socket growing real routes** once a document actually builds an
  agent-initiated daemon callback (Documented decision #9).
- **CoW-restore wiring into the diff/fork paths** — L18 left this as its own named gap (NL-47); a
  diff viewer would be a natural first consumer.
