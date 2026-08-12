# 10 — The web UI

`http://127.0.0.1:7717`. One Go binary serves it; there is no Node toolchain, no bundler, no
lockfile, and no build step beyond `go build ./...`.

**The web UI is a first-class surface, at parity with the TUI.** Anything you can do in the terminal
you can do in the browser: start work, chat with an agent, approve and reject, cancel, fork, follow
logs live, inspect a run, browse the event log, manage triggers and runners. Neither surface is
privileged, neither is a viewer for the other, and both are clients of the same API over the same
socket — which is what keeps them from diverging.

---

## Capability parity

Both surfaces do everything. A handful of things are genuinely *better* in one, and pretending
otherwise would push people to the wrong tool.

| Capability | TUI | Web | CLI | Better in |
| --- | --- | --- | --- | --- |
| start a run from prose | ✓ | ✓ | ✓ | — |
| run a named workflow with params | ✓ | ✓ | ✓ | — |
| chat with an agent, follow-ups | ✓ | ✓ | ✓ | — |
| see what is running, right now | ✓ | ✓ | ✓ | — |
| **approve / reject / answer a decision** | ✓ | ✓ | ✓ | **terminal** — no context switch when you are already in it |
| cancel a run, with compensation | ✓ | ✓ | ✓ | — |
| fork a run with overrides | ✓ | ✓ | ✓ | — |
| follow logs live | ✓ | ✓ | ✓ | — |
| inspect a node's input, output, transcript | ✓ | ✓ | ✓ | — |
| **read a diff at review quality** | look | ✓ | shell-out | **browser** — side-by-side, syntax, word-level, 62 files |
| **read a 40-turn transcript with search** | tail | ✓ | `$PAGER` | **browser** — it is a document |
| **compare two runs side by side** | table | ✓ | `--json` | **browser** — comparison is a two-column problem |
| **scan a 10 000-event timeline** | paged | ✓ | `--json` | **browser** — scannable at 200 columns |
| browse and query the event log | ✓ | ✓ | ✓ | — |
| manage triggers, pause a source | ✓ | ✓ | ✓ | — |
| manage runners, see health and drift | ✓ | ✓ | ✓ | — |
| host probe / doctor | ✓ | ✓ | ✓ | — |
| cost and gate-effectiveness reports | ✓ | ✓ | ✓ | — |
| **hand a diff to `delta` / `git difftool`** | ✓ | link | ✓ | **terminal** — your tools are already better |
| **work over SSH on a headless box** | ✓ | tunnel | ✓ | **terminal** |
| **sub-frame latency, no tab switch** | ✓ | ~ | ✓ | **terminal** |
| **a pasteable URL for an investigation** | `kairos open` | ✓ | ✓ | **browser** |
| scripting, cron, git hooks | — | — | ✓ | **CLI** |

Two consequences worth stating, because they are what parity actually costs:

- **The decision card is identical in both**, rendered from the same server-computed context, with the
  same evidence-before-controls ordering and the same typed-word confirmation. Presentation may differ;
  content may not. That was already the right call and it does not change.
- **Every capability must have a CLI verb.** Not because the CLI is the "real" interface, but because
  "if a UI can do it, `curl` can, and `kairos` does" is the rule that stops either UI growing a private
  API. Enforced by `TestUI_everyCallHasCLICounterpart`.

---

## How it works

```text
internal/web/
  server.go              routes, SSE hub, fragment handlers, auth middleware
  render.go              template funcs: dur, cost, id, sev, diffline, relTime
  templates/
    layout.gohtml        the only shell
    home.gohtml  runs.gohtml  run.gohtml  diff.gohtml  decision.gohtml
    compare.gohtml  events.gohtml  findings.gohtml  cost.gohtml
    runners.gohtml  sources.gohtml  doctor.gohtml  conversation.gohtml
    frag/                htmx swap targets — one file per fragment
      runrow.gohtml  timeline.gohtml  nodedetail.gohtml  findings.gohtml
      logtail.gohtml  header.gohtml  effects.gohtml  message.gohtml
  static/
    htmx.min.js          vendored, ~14 KB, checked in, version-pinned
    htmx-ext-sse.js      vendored
    app.css              ~700 lines: tokens, layout, diff colours. No framework.
    app.js               ~600 lines. Budgeted below.
```

```go
//go:embed all:templates static
var assets embed.FS
```

**Dev mode swaps the filesystem, not the code.** With `-tags dev` the template FS binds to
`os.DirFS("internal/web")` and re-parses per request; the release build parses the embedded FS once at
startup into a cached `*template.Template`. Five lines, and it deletes the "edit HTML, rebuild the Go
binary" complaint that otherwise drives people to reach for a bundler.

**Vendor htmx as a file, never a CDN.** A strict `Content-Security-Policy: default-src 'self'` is both
a security posture and a guarantee the page works with no network at all — which matters for a
local-first tool.

### Two kinds of route

```go
// Page: a full document. Bookmarkable. Renders its fragments inline on first paint.
mux.HandleFunc("GET /runs/{id}",              h.page(h.run))

// Fragment: a bare <div>. Never a full document. Always under /frag/.
mux.HandleFunc("GET /frag/run/{id}/timeline", h.frag(h.runTimeline))

// Mutation: a POST that returns the re-rendered fragment it affected.
mux.HandleFunc("POST /runs",                  h.mut(h.startRun))
```

A page renders its fragments server-side on first paint, so it is complete with JavaScript disabled and
htmx only takes over for updates. Every handler declares the API operation it reads and the CLI verb
that covers it; the route-map lint walks the table asserting all three exist.

---

## Transport

**SSE for server→client. Plain POST for client→server. Not WebSocket, not GraphQL subscriptions.**

This is a settled decision, and the rationale belongs in the document because it is not obvious and
someone will eventually want to "upgrade" it.

### Why SSE downstream

**The event log already has a monotonic, gap-free `global_seq`.** That single fact does most of the
work: `Last-Event-ID` gives **exact, replayable, gap-free resumption for free**, with no server-side
session state at all. Drop the connection mid-stream — close the laptop, lose Wi-Fi, restart the daemon
— and the browser reconnects with the last sequence it saw, and the server replays from precisely there
with a `WHERE global_seq > ?`.

A WebSocket would force us to *reimplement* that guarantee: sequence tracking in the client, a
replay-on-reconnect protocol, application-level heartbeats, a backpressure scheme, and a reconnect
ladder. We would be writing code to reach a property we already have.

Two smaller wins that follow: SSE is plain HTTP, so it inherits the same auth middleware, the same
proxy behaviour, and `curl -N` as a debugging tool; and the browser's `EventSource` reconnects on its
own, sending `Last-Event-ID` unprompted.

### Why POST upstream

Writes here are **low-frequency and idempotency-keyed**: a message, a decision, a cancel, a fork, a
pause. Not a keystroke stream. An ordinary POST that returns the re-rendered fragment is simpler than a
socket frame and gets HTTP status codes, conditional requests, retries, and `curl` for free — plus a
409 on a stale write instead of a bespoke conflict message.

```html
<form hx-post="/runs" hx-target="#running" hx-swap="afterbegin"
      hx-headers='{"Idempotency-Key":"{{ .FormNonce }}"}'>
  <input name="prompt" placeholder="describe a task…" autofocus>
</form>
```

The `Idempotency-Key` is minted server-side into the form, so a double-submit or a retried POST creates
one run, not two.

### One resumption story, not three

The TUI, the web page, and `kairos logs --follow` all resume the same way, against the same endpoint,
with the same semantics. Three transports would mean three reconnect implementations and three subtly
different definitions of "caught up" — and the one that is wrong is the one you find out about during
an incident.

### Why not GraphQL subscriptions

Rejected outright: a schema layer, resolvers, a client library, and codegen — which reintroduces exactly
the Node toolchain the stack decision removed, for a single user, to query a local SQLite file that
already has projections shaped for these screens.

### The one trigger that would change this call

If a genuinely **bidirectional, high-frequency** stream ever appears — live collaborative editing of a
workflow, or terminal passthrough that writes into a running agent's **stdin** — then SSE-plus-POST is
the wrong shape and a WebSocket becomes correct for *that channel only*. Name it, scope it, and leave
the event stream on SSE. Nothing in the current design needs it: an agent's stdin is written by the
engine, not by a human, and a decision is one POST.

### Log tailing is the one hand-written stream

htmx's swap model is wrong for logs — you want append-with-a-cap-and-a-scroll-lock, not replace.

```js
// static/app.js — ~60 lines
const es = new EventSource(`/api/v1/runs/${runID}/logs?node=${nex}&follow=1&offset=${off}`);
es.onmessage = (e) => {
  const atBottom = pane.scrollHeight - pane.scrollTop - pane.clientHeight < 4;
  pane.appendChild(document.createTextNode(e.data + "\n"));
  while (pane.childNodes.length > 10000) pane.removeChild(pane.firstChild);
  if (atBottom) pane.scrollTop = pane.scrollHeight;   // never autoscroll if scrolled up
  lastOffset = e.lastEventId;                          // a BYTE OFFSET, not a sequence
};
```

`Last-Event-ID` is a `global_seq` on the event stream and a **byte offset** on the log stream. Both are
exactly resumable.

### Events invalidate; they do not patch

```html
<div hx-ext="sse" sse-connect="/api/v1/runs/run_01A8x/events?follow=true">
  <div sse-swap="node.execution.completed,run.state.changed,constraint.evaluated"
       hx-get="/frag/run/run_01A8x/timeline"
       hx-trigger="sse:message">
    {{ template "frag/timeline" . }}
  </div>
</div>
```

The event triggers a **server re-render of the fragment**. Under htmx this rule is not a discipline
anyone can violate: the browser holds no model to patch, so projection logic can only exist in Go, next
to the engine that computed it. **The stack makes the most important realtime invariant structural
rather than aspirational**, and that is the main reason to keep it as parity raises the JS budget.

### No client-side virtualisation

A 10 000-event timeline pages server-side: `?from=<seq>&limit=200`. Client-side virtualisation exists to
avoid network round-trips, and on loopback against a memory-mapped local SQLite file a page fetch is
~1 ms. The justification was latency; there is none.

---

## The stack, reconsidered under parity

Parity is a real argument for a component framework, and it deserves an honest answer rather than a
restatement of the original decision.

**What changed.** As an inspector, the page needed ~200 lines of JavaScript. At parity it needs roughly
**~600**:

| | lines |
| --- | --- |
| log tail: `EventSource`, cap, scroll-lock | ~60 |
| keyboard model: `g`-prefix navigation, `/` search, `?` help, focus management | ~120 |
| command palette: fuzzy match over runs/tasks/flows, ULID resolve | ~110 |
| composer: optimistic echo, pending/failed states, `⇧⏎` newline, draft persistence | ~90 |
| diff viewer: expand context, file collapse, `[`/`]` navigation, deep-link scroll | ~80 |
| dialogs: fork, cancel-with-compensation, confirm-typed-word | ~70 |
| SSE connection state, backoff display, dropped-frame counter | ~70 |

**Why a framework still loses.** No Node toolchain, no `package.json`, no lockfile, no second CVE feed
to watch, no build step between editing a template and seeing it, and `go build ./...` continues to
produce the entire product. Against that, 600 lines of vanilla JS is a small, readable, dependency-free
budget — and the "events invalidate, they do not patch" rule stays *structurally* enforced, which is the
property most likely to be quietly lost in a rewrite to React, where a client-side store duplicating a
projection is the natural thing to build.

**The concrete trigger to revisit.** Either of:

1. the hand-written JS passes **~1500 lines**, or
2. any of it starts **holding derived state that duplicates a projection** — a client-side list of runs
   that must be kept in sync, a computed cost total, a locally-maintained timeline.

The second is the real signal; the first is a proxy for it. If either fires, a component framework is
the right answer and this decision should be superseded rather than defended — and the migration is
tractable precisely because all state lives server-side today.

---

## Auth and safety

**Localhost is not an auth mechanism.** Any web page you visit can issue requests to `127.0.0.1`, and so
can the agent you just spawned — it runs as your uid on the same machine. Now that the page can *start
work* and *approve decisions*, an unauthenticated local API is one CSRF away from being an
arbitrary-code-execution service.

| | Control |
| --- | --- |
| 1 | **Loopback bind only.** `127.0.0.1:7717`, never `0.0.0.0`. Any other address **refuses to start** without an acknowledgement string: `web: { listen: "0.0.0.0:7717", iUnderstandThisExposesAgentControl: "yes-lan-only-behind-a-firewall" }`. Same pattern as the isolation acknowledgement, deliberately. |
| 2 | **Per-start random token**, 32 bytes, at `~/.kairos/web-token` mode 0600, regenerated every `kairos serve`. Accepted as `Authorization: Bearer` (so `curl` and the CLI work) **or** a one-time `?t=` that sets an `HttpOnly; SameSite=Strict` cookie and **302s to strip the query**, so the token never lands in history or a pasted URL. `kairos open` and `kairos web` mint the URL, so you never type or see it. |
| 3 | **`Host` header allowlist** — `127.0.0.1:7717` and `localhost:7717` only. This is what blocks DNS rebinding, and it is the control people forget. |
| 4 | **`Origin` / `Sec-Fetch-Site` checked on every mutating request.** Cross-site rejected outright; never rely on `SameSite` alone. |
| 5 | **CSP** `default-src 'self'; script-src 'self'; img-src 'self' data:; frame-ancestors 'none'`, plus `Referrer-Policy: no-referrer`. Free, given everything is embedded. |

Two rules that matter more than the five:

- **The web token must never reach a workspace.** Not in any actor's environment, not in `input.json`,
  not in a rendered context file. An agent that can read the token can approve its own gate.
- **The agent-facing helper endpoint is a different socket with a strictly smaller route table.** Agents
  get `kairos check-output`, `artifact stage`, and `ask-human`; they do not get `approve`, `answer`,
  `publish`, `admin`, or anything that starts a run. `TestArchitecture_agentSocketRouteSubset` asserts
  the subset. This is the single most important safety detail in the surface — and the same reasoning
  gives a runner its own subset test ([`07-runners.md`](07-runners.md)).

---

## The first sixty seconds in the browser

The browser is now a legitimate entry point, so it needs the same treatment the terminal gets.

```console
$ kairos web
opening http://127.0.0.1:7717/?t=… (one-time)
```

`kairos web` ensures the daemon is running, mints a one-time URL, and opens your browser. If you
navigate to `127.0.0.1:7717` by hand with a valid cookie, you land on the same place.

**If the daemon is not running**, the browser gets a connection refused — a blank error page from
Chrome, which is a bad first experience and not something the daemon can fix from the grave. Two
mitigations, both cheap:

- `kairos web` (and `kairos open`) **start the daemon** if it is not running, exactly as the TUI does.
  The documented way in is a command, not a bookmark.
- The launchd/systemd user unit from `kairos up --install` keeps the daemon alive across logins, which
  makes the bookmark reliable. `kairos doctor` says whether it is installed.

First paint on a fresh install, before any run exists:

```text
┌────────────────────────────────────────────────────────────────────────────────┐
│  kairos     orders-service ▾            ⚑ 0        $0.00 today      ● live      │
├────────────────────────────────────────────────────────────────────────────────┤
│                                                                                │
│   Nothing running.                                                             │
│                                                                                │
│   Describe a task below. I will work in a clone, on a branch, and ask you       │
│   before anything leaves this machine.                                         │
│                                                                                │
│   ┌──────────────────────────────────────────────────────────────────────┐     │
│   │ add cursor pagination to GET /orders, no offsets                  ⏎  │     │
│   └──────────────────────────────────────────────────────────────────────┘     │
│                                                                                │
│   People usually start with                                                    │
│     · fix the flaky test in services/orders                                    │
│     · why is TestListCursor failing?          (asks, does not change anything)  │
│     · review my last 3 commits                                                 │
│                                                                                │
│   This repo   47 Go files · 31 tests · 14 TODOs · last commit 2h ago            │
│                                                                                │
│   ⚠  Agents run on this machine as you, with your files and credentials.       │
│      Nothing is sandboxed by default.  [ read what this means ]  [ accept ]     │
└────────────────────────────────────────────────────────────────────────────────┘
```

The isolation acknowledgement is the **same one-time recorded event** as in the TUI, not a second
consent flow — accept it in either surface and the other stops asking. The scope confirmation shown
before a repo's first run ("I will: workflow, actor, workspace, scope, budget, gates — your checkout is
not touched") appears here too, with the same `don't ask again`.

---

## Route map

Every row: what it shows, the API operation behind it, and the CLI verb that covers the same ground.

| Path | Shows / does | API | CLI |
| --- | --- | --- | --- |
| `/` | home: composer, waiting-on-you, running, today | `GET /runs?state=active`, `GET /human-tasks?state=open` | `kairos status`, `kairos inbox` |
| `POST /runs` | **start a run from prose or a flow** | `POST /runs` | `kairos do`, `kairos run` |
| `/runs` | run list, filtered | `GET /runs?…` | `kairos ls runs` |
| `/runs/{id}` | run detail: timeline, cost, effects, findings | `GET /runs/{id}` | `kairos show` |
| `/runs/{id}/nodes/{nex}` | node inspector: input, output, transcript | `GET /runs/{id}/nodes/{nex}` | `kairos show` |
| `/runs/{id}/diff` | the diff viewer | `GET /runs/{id}/diff` | `kairos diff` |
| `/runs/{id}/logs/{nex}` | log tail, following | `GET /runs/{id}/logs?node=` | `kairos logs --follow` |
| `/runs/{id}/tree` | children and forks | `GET /runs/{id}/tree` | `kairos tree` |
| `POST /runs/{id}/cancel` | **cancel, optionally compensating** | `POST /runs/{id}/cancel` | `kairos cancel` |
| `POST /runs/{id}/fork` | **fork with overrides** | `POST /runs/{id}/fork` | `kairos fork` |
| `POST /runs/{id}/say` | **inject a message into a live session** | `POST /runs/{id}/say` | `kairos say` |
| `/compare?a=&b=` | two runs side by side | `GET /runs/{a}`, `GET /runs/{b}` | `kairos diff a b` |
| `/t/{taskID}` | **the decision page** | `GET /human-tasks/{id}` | `kairos show` |
| `POST /t/{taskID}/answer` | **approve / reject / answer** | `POST /human-tasks/{id}/answer` | `kairos approve\|reject\|answer` |
| `/c/{convID}` | conversation, with a composer | `GET /conversations/{id}` | `kairos show` |
| `POST /c/{convID}/messages` | **post a message** | `POST /conversations/{id}/messages` | `kairos say` |
| `/findings` | open findings across runs | `GET /findings?state=open` | `kairos ls findings` |
| `/cost` | spend by day, project, model | `GET /costs?groupBy=` | `kairos cost` |
| `/gates` | gate effectiveness, 30d | `GET /gates/report` | `kairos gates report` |
| `/events` | the event log explorer | `GET /events?…` | `kairos events` |
| `/sources` | trigger health, cursors | `GET /sources` | `kairos src ls` |
| `POST /sources/{id}/pause` | **pause / resume / poll now** | `POST /sources/{id}/…` | `kairos src pause\|poll` |
| `/runners` | runners, health, toolchain, in-flight | `GET /runners` | `kairos runner ls` |
| `/doctor` | host probe, toolchain, disk | `GET /doctor` | `kairos doctor` |
| `/flows` | published workflows, validation | `GET /flows` | `kairos flow ls` |

Mutating routes are the nine marked in bold. Each requires the `Origin` check and carries an
`Idempotency-Key`; the one that answers a decision additionally requires the typed decision word.

---

## Screens

### Home

```text
┌────────────────────────────────────────────────────────────────────────────────┐
│  kairos     orders-service ▾        ⚑ 2        $1.84 today        ● live       │
├────────────────────────────────────────────────────────────────────────────────┤
│  ┌──────────────────────────────────────────────────────────────────────┐      │
│  │ describe a task…                                                  ⏎  │      │
│  └──────────────────────────────────────────────────────────────────────┘      │
│                                              flow ▾   runner ▾   budget ▾      │
│                                                                                │
│  WAITING ON YOU                                                                │
│  ┌──────────────────────────────────────────────────────────────────────────┐  │
│  │ ⚠ high   push branch + open PR · pagination            4m ago    review → │  │
│  │          run_01A8x · 3 files +148 −22 · gates 4/4 · 1 medium finding      │  │
│  ├──────────────────────────────────────────────────────────────────────────┤  │
│  │          "should cancel be idempotent?"                22m ago   answer → │  │
│  └──────────────────────────────────────────────────────────────────────────┘  │
│                                                                                │
│  RUNNING                                                        3 · 2 runners  │
│  ┌──────────────────────────────────────────────────────────────────────────┐  │
│  │ run_01A8x  add pagination to GET /orders      ●●●●●●○○○  6/9  4m12s $0.71 │  │
│  │            test-and-fix · implementer · attempt 2 · local     logs  ⏹ ⑃  │  │
│  │ run_01A9k  review the pagination change       ●●○○       2/4  0m38s $0.09 │  │
│  │ run_01B2c  ↳ write-tests (child)              ◷ waiting: sibling          │  │
│  └──────────────────────────────────────────────────────────────────────────┘  │
│                                                                                │
│  TODAY                                                            5 done  →    │
│  ✓ run_01A7m  add @bob to CODEOWNERS          2m    $0.06   PR #418            │
│  ✗ run_01A6d  bump go deps                    1h    $0.31   failed at: lint    │
│                                                                                │
│  ┌─ SPEND ──────────────────┐  ┌─ GATES, 30d ─────────────────────────────┐   │
│  │ ▁▂▅▃▇▄▂  $18.40 this wk  │  │ lint      61 fires  12 caught  ⚠ 0 dwell │   │
│  │ 31% on failed runs   ⚠   │  │ security   9 fires   4 caught             │   │
│  └──────────────────────────┘  └───────────────────────────────────────────┘   │
└────────────────────────────────────────────────────────────────────────────────┘
```

| Feature | Note |
| --- | --- |
| **the composer is the first thing on the page** | autofocused on an empty home; this is the entry point, not a link target |
| flow / runner / budget selectors next to it | collapsed by default; prose plus defaults is the common path |
| waiting-on-you first, with the risk headline inline | never a bare "1 task waiting" |
| inline `logs`, `⏹` cancel, `⑃` fork on each running row | both destructive ones open a dialog, never one click |
| which runner each run is on | matters once remote runners exist ([`07-runners.md`](07-runners.md)) |
| `◷ waiting: sibling` | the reason is always named. Never a spinner. |
| spend sparkline + **share spent on failed runs** | the number nobody tracks and everybody should |
| gate effectiveness card | 100% approval with zero evidence-dwell is flagged for deletion |
| `● live` / `◐ 45s` / `◌ engine not responding` | quiet is not the same as disconnected |

Sparklines are inline SVG generated in Go — ~30 lines of `render.go`, no charting library.

### Run list

```text
┌────────────────────────────────────────────────────────────────────────────────┐
│  runs                                                        / to search        │
├────────────────────────────────────────────────────────────────────────────────┤
│ state ▾  project ▾  flow ▾  runner ▾  since ▾  cost > ▾      42 runs  ⤓ csv    │
├──────────┬──────────────────────────┬─────────┬───────┬───────┬────────┬───────┤
│ state    │ objective                │ flow    │ nodes │ dur   │ cost   │ runner│
├──────────┼──────────────────────────┼─────────┼───────┼───────┼────────┼───────┤
│ ● running│ add pagination to GET /… │ implem… │ 6/9   │ 4m12s │ $0.71  │ local │
│ ⚑ waiting│ push branch + open PR    │ implem… │ 8/9   │ 12m   │ $2.90  │ local │
│ ✓ ok     │ add @bob to CODEOWNERS   │ adhoc   │ 3/3   │ 2m01s │ $0.06  │ local │
│ ✗ failed │ bump go deps             │ implem… │ 4/9   │ 1h04m │ $0.31  │ macmini│
│ ◑ degrad…│ cross-repo rename        │ coord   │ 7/9   │ 22m   │ $4.10  │ mixed │
└──────────┴──────────────────────────┴─────────┴───────┴───────┴────────┴───────┘
```

Filters are querystring state, so a filtered list is a bookmarkable URL. Sort by any column. `⤓ csv`
exports what you are looking at — the cheapest possible answer to "I want this in a spreadsheet", and it
removes a class of feature request.

### Run detail

```text
┌────────────────────────────────────────────────────────────────────────────────┐
│  run_01A8x   implement-task@3   ✓ succeeded   4m12s   $0.71   ⑃ fork  ⧉ copy   │
│  add cursor pagination to GET /orders (no offsets)                             │
│  workspace ~/.kairos/work/run_01A8x/repo · clone · clean · runner local        │
│  parent —   children run_01B2c ✓   forks →run_01A8y                            │
├───────────────────────────────────┬────────────────────────────────────────────┤
│ TIMELINE                          │ NODE  implement                            │
│                                   │ ─────────────────────────────────────────  │
│ ● 00:00.0  run.started            │ actor      implementer (claude-sonnet-4-6) │
│ ✓ 00:00.4  plan          rule     │ attempt    2 of 3      iteration 1         │
│ ✓ 00:31.2  read-context  30.8s    │ session    sess_01A8 · 1 compaction ⚠      │
│ ✓ 02:04.7  implement     93.5s ◀  │ cost       $0.38   41 tool calls           │
│   ├ gates  build ✓ lint ✓         │ runner     local                           │
│   │        no-todos ✓ scope ✓     │                                            │
│ ✗ 02:41.0  test          exit 1   │ INPUT                            {} raw    │
│ ↻ 02:41.0  test-and-fix  att 2    │  tasks[4]  from $.outputs.plan.tasks       │
│ ✓ 03:58.9  test          77.9s    │  findings[0]                               │
│ ⚑ 04:02.1  push-approval  12m     │                                            │
│ ✓ 04:12.0  run.succeeded          │ OUTPUT                     ✓ schema valid  │
│                                   │  branch  kairos/pagination                 │
│ EFFECTS                           │  sha     9f2ac41                           │
│ git.commit  2 commits  reversible │  summary "Adds cursor pagination…"         │
│ git.push    origin/…   ⚠ irrevers.│                                            │
│                                   │ ARTIFACTS                                  │
│ FINDINGS  1                       │  diff 4.1 KB · transcript 182 KB · logs    │
│ medium quality  2 TODOs  list.go  │ [logs] [transcript] [diff] [copy prompt]   │
└───────────────────────────────────┴────────────────────────────────────────────┘
```

| Feature | Note |
| --- | --- |
| timeline with gate results nested under their node | one glance answers "what rejected it" |
| `↻ attempt 2 of 3` and `1 compaction ⚠` on the primary view | the two highest-value under-surfaced signals, both free to compute |
| input with **selector provenance** | `tasks[4] from $.outputs.plan.tasks` answers "where did this come from" without reading the flow |
| `✓ schema valid`, linking to the schema | validation is visible, not assumed |
| effects with reversibility marked | irreversible ones in the risk colour |
| the runner each node ran on | a node-level detail once runners are plural |
| `{} raw` toggles the raw JSON | always available, never the default |
| `⧉ copy` copies the run URL; `copy prompt` copies the exact rendered prompt | reproducing a run by hand is a first-class need |
| `⑃ fork` opens a dialog with actor/param overrides and a cost estimate | forking spends money |

Clicking a timeline row swaps the inspector fragment — no page load, no client state.

### The diff viewer

```text
┌────────────────────────────────────────────────────────────────────────────────┐
│  run_01A8x · diff vs main@3f2a1c9        3 files  +148 −22       ⇱ unified ▾   │
│  declared scope services/orders/**            ✓ no violations                  │
├────────────────────────────────────────────────────────────────────────────────┤
│ ▾ services/orders/list.go                                    +121 −18          │
│ ┌──────────────────────────────────┬───────────────────────────────────────┐   │
│ │ 38   func (s *Svc) List(         │ 38   func (s *Svc) List(              │   │
│ │ 39 -   ctx context.Context,      │ 39 +   ctx context.Context,           │   │
│ │ 40 -   offset, limit int,        │ 40 +   cursor string, limit int,      │   │
│ │ 41   ) ([]Order, error) {        │ 41   ) ([]Order, string, error) {     │   │
│ │      ⋯ 24 unchanged lines ⋯  ⊕   │      ⋯ 24 unchanged lines ⋯  ⊕        │   │
│ └──────────────────────────────────┴───────────────────────────────────────┘   │
│ ▸ services/orders/list_test.go                                 +25 −4          │
│ ▸ services/orders/repo.go                                       +2 −0          │
├────────────────────────────────────────────────────────────────────────────────┤
│  also available:  $PAGER ↗   git difftool ↗   gh pr view ↗                     │
└────────────────────────────────────────────────────────────────────────────────┘
```

| Feature | Note |
| --- | --- |
| side-by-side or unified, remembered | a `localStorage` preference — presentation only, never derived state |
| **syntax highlighting server-side** | `github.com/alecthomas/chroma`, an **approved dependency** for exactly this: it renders to spans in Go, so there is no client highlighter, no CSP exception, and no 400 KB of JavaScript |
| word-level intra-line highlight | computed in Go from a per-line diff |
| collapsed context, `⊕` to expand | server-rendered on expand |
| per-file collapse, sticky file header, `[` / `]` to move | a 62-file diff stays navigable |
| **scope-violation banner** | a file outside the node's declared `workspacePaths` gets a full-width banner above the file list in the risk colour. The most useful computable diff signal there is. |
| binary and generated files collapsed by default | with the reason named (`binary`, `>2000 lines`, `matches generated glob`) |
| `?file=` and `?line=` deep links | point someone at a specific hunk |
| shell-out links in the footer | not an apology — `delta` is genuinely better, and linking out beats imitating it badly |

### The decision page

Identical content to the TUI's card, same ordering, same rules. See
[`09-cli-and-tui.md`](09-cli-and-tui.md) for the terminal rendering and the full reasoning.

```text
┌────────────────────────────────────────────────────────────────────────────────┐
│  DECISION   push pagination branch and open PR              open 12m   ⚑ high  │
│  objective  add cursor pagination to GET /orders (no offsets)                   │
│  asked by   node push-approval · run_01A8x · implement-task@3                  │
├────────────────────────────────────────────────────────────────────────────────┤
│ ┌─ RISK ─────────────────────────────────────────────────────────────────────┐ │
│ │ ⚠ irreversible   2 effects, listed below                                   │ │
│ │ ⚠ findings       1 medium (quality) · 0 high                               │ │
│ │ ✓ gates          build ✓  test ✓ 31/31  lint ✓  scope ✓                     │ │
│ │ ◷ cost           $2.90 of $10 limit                                        │ │
│ │ ↻ attempts       `test` took 3 attempts                                    │ │
│ │ ⚠ session        1 compaction — the agent forgot part of its context       │ │
│ └────────────────────────────────────────────────────────────────────────────┘ │
│ ┌─ HOST EFFECTS ── runs on this machine, unsandboxed ────────────────────────┐ │
│ │ ⚠ git push      origin kairos/pagination      (new remote ref)             │ │
│ │ ⚠ gh pr create  acme/orders-service           (irreversible: notifies)     │ │
│ │ ✓ outside repo  none                                                       │ │
│ │ ✓ your checkout ~/code/orders-service untouched and clean                  │ │
│ │ ✓ commands      go build, go test, gofmt      (no package installs)        │ │
│ │ ◦ network       api.anthropic.com, proxy.golang.org, github.com            │ │
│ │ ◦ runner        local                                                      │ │
│ └────────────────────────────────────────────────────────────────────────────┘ │
│ ┌─ CHANGED  3 files +148 −22 ───────────────────────────── open full diff ↗ ──┐│
│ │  +121 −18  services/orders/list.go        [inline diff, first 40 lines]     ││
│ └────────────────────────────────────────────────────────────────────────────┘│
│ ┌─ FINDINGS 1 ───────────────────────────────────────────────────────────────┐│
│ │ medium quality  2 TODOs added  list.go:88,140              [show in diff]   ││
│ └────────────────────────────────────────────────────────────────────────────┘│
├────────────────────────────────────────────────────────────────────────────────┤
│  YOUR DECISION                                                                 │
│  ( ) approve      ( ) request changes      ( ) reject                          │
│  reason  [                                                              ]      │
│  type the decision to confirm  [        ]              [ submit ]  (disabled)   │
└────────────────────────────────────────────────────────────────────────────────┘
```

| Rule | How the browser enforces it |
| --- | --- |
| evidence before controls, in **DOM order** | the form is the last element; no CSS reorders it, no `tabindex` overrides it |
| the form is unreachable until evidence rendered | `fieldset[disabled]` until every evidence fragment resolves |
| **no single-key approve, ever** | submit stays disabled until the decision word is typed; autocomplete off; `⌘⏎` works only inside the form |
| risk acceptance is a separate control | rendered only for high/critical findings, naming the finding id, never bundled into submit |
| **failed evidence blocks the form** | a fragment that 500s renders its error and keeps the fieldset disabled, with a retry |
| the page never claims you read the diff | the recorded answer says which panes rendered and for how long |
| **dwell measurement** | the one thing the browser does better: `IntersectionObserver` gives exact per-pane visible time, feeding `kairos gates report` |

`POST /t/{id}/answer` validates against the task's JSON Schema **server-side**; client-side hints only.
The client hints, the server decides — otherwise the CLI, the TUI, and this page grow three different
notions of a valid answer.

### Conversation

```text
┌────────────────────────────────────────────────────────────────────────────────┐
│  #pagination            3 runs · $2.90 · workspace clean            ● live      │
├────────────────────────────────────────────────────────────────────────────────┤
│  you                                                                    10:02  │
│    add cursor pagination to GET /orders. no offsets.                           │
│                                                                                │
│  implementer                                     run_01A8x ✓ 4m12s $0.71       │
│    ┌────────────────────────────────────────────────────────────────────────┐  │
│    │ 3 files +148 −22 · services/orders/**                                  │  │
│    │ gates build ✓ test ✓ 31 lint ✓ scope ✓                                  │  │
│    │ branch kairos/pagination (2 commits, not pushed)                        │  │
│    │ [diff] [transcript] [run]                                              │  │
│    └────────────────────────────────────────────────────────────────────────┘  │
│    ▸ 41 tool calls · 3 milestones                                    expand ⌄  │
│                                                                                │
│  ┌─ DECISION · push branch and open PR ─────────────────────── open 12m ⚑ ───┐ │
│  │ ⚠ irreversible: git push, gh pr create   ✓ gates 4/4   1 medium finding   │ │
│  │                                                            review →       │ │
│  └──────────────────────────────────────────────────────────────────────────┘ │
├────────────────────────────────────────────────────────────────────────────────┤
│  @implementer  also add a total-count header                              ⏎    │
└────────────────────────────────────────────────────────────────────────────────┘
```

The composer is here too. `⏎` sends, `⇧⏎` newlines, the message echoes optimistically marked pending and
reconciles when its sequence is assigned — **never silently dropped**; a failed post renders as failed
with a retry. Unaddressed messages go to the sole non-human participant; with more than one, the
composer asks rather than guessing. Progress coalesces to a count with milestones broken out. Drafts
persist per conversation in `localStorage` (presentation state, not derived state).

### Compare

```text
┌────────────────────────────────────────────────────────────────────────────────┐
│  compare        run_01A8x  (opus)          ⇄        run_01A8y  (sonnet, fork)   │
├───────────────────────────────┬────────────────────────────────────────────────┤
│ forked at seq 5 · same inputs · same definition@3 · workspace @pre-implement-1  │
├───────────────────────────────┼────────────────────────────────────────────────┤
│ outcome     ✓ succeeded       │ ✓ succeeded                                    │
│ duration    4m12s             │ 6m48s                                          │
│ cost        $0.71             │ $0.19                          ← 3.7× cheaper  │
│ attempts    implement 2       │ implement 3                                    │
│ gates       4/4 first pass    │ 4/4 on second pass                             │
│ findings    1 medium          │ 3 medium, 1 high               ← worse         │
│ diff        3 files +148 −22  │ 4 files +171 −22                               │
│ compactions 1                 │ 0                                              │
│ runner      local             │ local                                          │
├───────────────────────────────┴────────────────────────────────────────────────┤
│  DIFF OF DIFFS                                                                 │
│  both changed   list.go  list_test.go  repo.go                                 │
│  only in B      services/orders/cursor_util.go  (+23)                          │
│  [side-by-side the two diffs ↗]                                                │
└────────────────────────────────────────────────────────────────────────────────┘
```

The **diff of diffs** is what makes forking worth using: it answers *what did the cheaper model
actually do differently* without reading two full diffs. Drift, if any, is annotated at the top in the
risk colour — a fork whose workspace came from a different moment is a state difference read as a model
difference, and the comparison must say so rather than look clean. When the two runs ran on different
runners, that is called out for the same reason.

### Findings, cost, gates, sources, doctor

Five small pages, each a filtered table plus a detail fragment. What each one *answers*:

| Page | Answers |
| --- | --- |
| `/findings` | *what is still open across every run*, grouped by constraint and by file |
| `/cost` | spend by day / project / model / actor, **share spent on failed runs**, and subscription-window utilisation (*am I using what I already pay for*) |
| `/gates` | fires per 100 runs, true-catch rate, waiver rate, evidence dwell. A constraint that never fires is broken; one always waived is wrong |
| `/sources` | per-trigger health, last poll, cursor, consecutive errors **verbatim**, dedupe-key hit rate, and pause/resume/poll-now buttons |
| `/doctor` | tool versions with paths, filesystem CoW capability, free disk, `RLIMIT_NOFILE`, sandbox availability, isolation posture, and **toolchain drift** since a given run |

### The event log explorer

```text
┌────────────────────────────────────────────────────────────────────────────────┐
│  events    type ▾  run ▾  correlation ▾  since ▾   seq ≥ [        ]   ⤓ ndjson │
├──────────┬──────────────────────┬──────────────────────────────────────────────┤
│ seq      │ type                 │ payload                                      │
├──────────┼──────────────────────┼──────────────────────────────────────────────┤
│ 184102 ▸ │ node.execution.start │ {node:"implement",attempt:2,…}               │
│ 184103 ▸ │ process.spawning     │ {tool:"claude",pgid:41233,runner:"local",…}  │
│ 184118 ▸ │ constraint.evaluated │ {id:"lint",result:"pass",exit:0,dur:2841ms}  │
│ 184119 ▾ │ effect.attempted     │ {action:"gh.pr.create",key:"run_01A8x:push:1"}│
│          │   caused by 184118 · correlation gh-issue-421 · actor engine        │
│          │   [full payload] [causal tree] [jump to run]                        │
└──────────┴──────────────────────┴──────────────────────────────────────────────┘
```

Plus **"explain this run"**: the causal tree rendered as an indented graph from `causation_seq`, which
answers *why did it do that* in a way no log grep does. `⤓ ndjson` exports the filtered set, so anything
this page cannot answer is answerable with `jq`. This is the read half of the time-travel debugger; the
write half (breakpoints, variable injection) stays in the TUI, and each such action remains an
attributable event.

### Runners

See [`07-runners.md`](07-runners.md) for the model. Kinds are `local`, `ssh`, and `serve`.

```text
┌────────────────────────────────────────────────────────────────────────────────┐
│  runners                                                     2 healthy · 1 down│
├────────────┬────────┬──────────┬────────┬──────────────┬────────────┬──────────┤
│ name       │ kind   │ health   │ in use │ toolchain    │ workspaces │ last seen│
├────────────┼────────┼──────────┼────────┼──────────────┼────────────┼──────────┤
│ local      │ local  │ ● healthy│ 2 / 4  │ go 1.25 ✓ …  │ 3 · 12 GB  │ now      │
│ macmini    │ serve  │ ● healthy│ 1 / 4  │ go 1.24 ⚠ …  │ 1 ·  4 GB  │ 3s ago   │
│ beelink    │ ssh    │ ◌ down   │ 0 / 6  │ —            │ 2 · 8 GB ⚠ │ 14m ago  │
│            │        │ ssh: connect to host beelink.lan port 22: connection ref…│
│            │        │ 2 runs pinned here are Blocked{runner-gone}  → [options] │
└────────────┴────────┴──────────┴────────┴──────────────┴────────────┴──────────┘
```

The columns that earn their place: **toolchain per runner with drift flagged** (`go 1.24 ⚠` where the
flow requires `>= 1.25`), **workspaces held** — because a run is pinned to its runner for life and a
down runner holding two workspaces is *why* those runs cannot proceed — and the connection error
**verbatim** rather than a status word. `[options]` surfaces the three operator choices for a
permanently-gone runner (cancel with compensation, fork onto another runner with a loud
`fork.workspace.unavailable`, or bring it back); none of them happen automatically.

---

## Cross-cutting

### Six screen states

| State | Rule |
| --- | --- |
| **loading** | a skeleton of the right *shape*, never a spinner in a layout-shifting box. On loopback most fragments never show it. |
| **empty** | says what would create one: *"No runs yet — describe a task above, or drop a file in `~/.kairos/inbox/`"*. Never a bare "no data". |
| **partial** | the fragments that loaded are shown; the failed one shows its own error. **Exception: the decision page, where partial degrades to blocked.** |
| **error** | the message verbatim, the correlation id, and a retry. Never "something went wrong". |
| **forbidden** | policy denied it, with the rule and its `reason`. Survives because policies constrain actors. |
| **stale** | the daemon is not responding; the page says so and stops pretending. |

**`Waiting` and `Degraded` are domain states, not UI states.** They never render as an error and never
as a spinner. A waiting run renders as *waiting, on this, since then* — a run parked on a human for
three days is the system working correctly, and rendering it as a problem trains you to ignore the
colour that means "problem".

**Quiet is not disconnected.** `● live` means the SSE stream is open; `◐ 45s` means open and nothing has
happened for 45 seconds; `◌` means the connection dropped, with a reconnect countdown. Conflating the
second and third teaches you to distrust the page.

### Look and feel

- **Dark mode via `prefers-color-scheme`** over a token set in `app.css`. No toggle: the OS already has
  one, and a toggle needs storage, a flash-of-wrong-theme fix, and a preference to sync.
- **Contrast verified in CI** over the token set, so a palette change cannot silently fail it.
- **Responsive to tablet width; explicitly not a phone app.** Below ~700 px the two-column layouts
  stack and the diff viewer forces unified mode. There is no phone target because reaching the page from
  a phone needs a public endpoint, which the design refuses.
- **`prefers-reduced-motion` honoured**, and no motion is needed to understand anything here.
- Monospace for identifiers, paths, commands, and diffs. Proportional for prose.

### Keyboard

Parity includes keyboard use, so the map is close to the TUI's.

| Key | |
| --- | --- |
| `⌘K` / `ctrl-K` | command palette: fuzzy over runs, tasks, flows, runners; paste any ULID to resolve it |
| `/` | focus search on a list page |
| `c` | focus the composer (home, conversation) |
| `g` then `h` `r` `f` `k` `e` `s` `n` `d` | home · runs · findings · cost · events · sources · runners · doctor |
| `j` / `k` | move selection; `⏎` opens; `⇧⏎` opens in a new tab |
| `[` / `]` | previous / next file in the diff viewer |
| `u` / `s` | unified / side-by-side toggle in the diff viewer |
| `a` | jump to the oldest waiting decision |
| `l` | logs for the selected run |
| `y` | copy the selected id — **`y` is yank and is never yes** |
| `f` | fork dialog for the selected run (a dialog, not an action) |
| `esc` | close a dialog, clear a filter, leave the composer |
| `?` | keyboard help, listing bindings for the current page |

**No single key mutates anything, and there is no approve shortcut at all.** The destructive dialogs
(cancel, fork) and the decision form all require an explicit confirmation, and the decision form
requires the typed word.

### Deep links

A first-class feature. Every entity has a stable URL; `kairos open <id>` resolves any prefixed ULID —
run, node execution, task, conversation, event sequence, finding, runner — to the right page and mints
the one-time token. An investigation becomes a thing you can point at next month.

### Accessibility

**Kept as hard requirements**, because each prevents a specific failure:

- Focus order on the decision page follows DOM order, evidence before controls, never reordered.
- No global approve shortcut; risk acceptance never bundled into submit.
- Focus is never lost on a fragment swap (`hx-preserve` on the focused element).
- Live regions announce state changes and **never** steal focus or move the caret. Progress is never
  announced — a screen reader reading 41 tool calls aloud is a denial of service.
- Colour is never the sole carrier of meaning: every severity has a glyph and a word.
- Contrast verified in CI. Keyboard-complete: every action reachable without a mouse.

**Dropped, with reasoning:** WCAG 2.2 AA as a certified target with an audit trail, `axe-core` in
Playwright over every screen × state, a per-release manual screen-reader pass, and 44×44 px touch
targets. Those are the costs of a compliance *programme* for a multi-user product; this is a
single-user local tool. What is kept is the subset that prevents rubber-stamping and keyboard traps.

**Added:** the line-oriented CLI path (`kairos show`, `kairos inbox`, `kairos logs`) is **first-class and
permanently supported**, not a degradation. A full-screen TUI is hostile to a screen reader; the
line-oriented mode is the accessible surface.

---

## Non-goals

| Not | Because |
| --- | --- |
| a workflow authoring environment | flows are YAML in a repo, edited in your editor, reviewed in a diff. A visual DAG editor is a large surface producing worse artifacts. The page *validates and shows* flows; it does not edit them. |
| a log viewer | the log is a capped tail plus a file on disk. `grep` and `$PAGER` are right there and better. |
| a chat product | no reactions, no presence, no DMs, no threads. The conversation exists to direct work and record decisions. |
| themeable | one palette, two modes. Theming is a support surface with no payoff for one user. |
| multi-user | no accounts, no permissions, no sharing model, no org switcher. Policies constrain *actors*, not people. |
| a phone app | would require a public endpoint. To be paged with the laptop closed, use `ntfy` ([`09-cli-and-tui.md`](09-cli-and-tui.md)). |
| a charting library | what is left is one sparkline and tables — ~30 lines of inline SVG. Adding a charting library would be the first crack in "no Node". |
| a terminal emulator | no shell passthrough into a running agent. That is the one thing that would justify a WebSocket, and nothing needs it. |

---

## Effort

| Group | Days |
| --- | --- |
| server, layout, auth middleware, SSE hub, fragment + mutation conventions, dev-mode FS | 5 |
| home with composer + run list + filters | 4 |
| run detail: timeline, node inspector, effects, findings, artifacts | 5 |
| **diff viewer**: side-by-side, chroma, collapse, scope banner, deep links | 6 |
| **decision page**: risk, host effects, focus order, dwell measurement, submit | 4 |
| conversation with composer, optimistic echo, message kinds | 3 |
| mutations: cancel, fork, say, source pause — dialogs and idempotency | 3 |
| compare + diff-of-diffs | 3 |
| event explorer + causal tree | 3 |
| findings / cost / gates / sources / runners / doctor / flows | 5 |
| command palette + keyboard model | 3 |
| CSS tokens, dark mode, contrast CI, a11y pass | 3 |
| `TestUI_everyCallHasCLICounterpart` + handler tests | 3 |
| **total** | **~50** |

Parity costs about 11 days more than an inspector would have. **If you must stage it**, ship in this
order — each stage is independently useful and none leaves a half-surface:

1. **Read-only core** (10d): server, home, run list, run detail. The page is worth opening.
2. **The two things the terminal cannot do** (10d): diff viewer, decision page. Now it earns its keep.
3. **Parity mutations** (6d): composer, cancel, fork, say. Now it is a surface rather than a viewer.
4. **Investigation** (9d): compare, event explorer, conversation.
5. **Operations and polish** (15d): findings, cost, gates, sources, runners, doctor, palette, a11y.

Do not stop between 2 and 3 for long. A page that shows everything and can act on nothing is the
inspector framing this document deliberately replaced, and it will read as a bug.
