# 07 — Surfaces

**TUI-first, with an embedded web page that is an inspector rather than a second console.**

The split is by *job*, not by completeness — which is what stops the two drifting into half-surfaces:

| | **TUI** (`kairos`) | **Web** (`127.0.0.1:7717`) |
| --- | --- | --- |
| job | **do the work; unblock it** | **read wide evidence** |
| mode | open all day, typed into | opened from a link for 90 seconds |
| content | what's running, conversation, decisions, run timeline, log tail | diffs, long transcripts, side-by-side compare, 10k-event timelines |
| shape | 80–120 columns, dense, keyboard | as wide as your monitor |

The original design had two browser surfaces (a conversational Console and an Operations UI) split
because they were two jobs for two moods. Locally it is one person, one localhost origin, and no vendor
to remove a dependency on — and the *Console* job (unblock work, glanced at forty times a day) is the
job a terminal does best and a browser tab does worst. So Console moves to the terminal, and the web app
becomes what Operations was: the visual, wide, drill-down surface.

The one component that must exist in **both** is the decision card: a 3000-line diff approval genuinely
wants a browser, and a `git push` approval genuinely wants to be answered without leaving the terminal.
Both render from the same server-computed decision context, so content cannot drift even where
presentation does.

---

## The command surface

Around 30 verbs, down from 94. Hot-path verbs are one word with no noun.

```text
kairos                          open the TUI (starts the daemon if needed); prints status if not a TTY

  work
  kairos do "<task>"            start a run from prose — the most-used verb
  kairos run <file> [--k v]     run a named workflow
  kairos ls [runs|pending|src]  list; bare = active runs
  kairos show <id>              polymorphic detail: run, task, source, workspace, event
  kairos logs [<run>]           interleaved output; --follow; --events for the typed stream
  kairos diff <run> [<run>]     the change it produced; two ids = compare
  kairos open <run>             open the run's workspace in $EDITOR, or print its path
  kairos say <run> "<text>"     inject a message into a live run's session
  kairos cancel <run>           stop it; --compensate to unwind applied effects
  kairos resume <run>           un-park a Waiting or Interrupted run

  humans
  kairos inbox                  your decision queue; --count for shell prompts; --json --watch
  kairos approve <task>         requires typing the decision word for irreversible tasks
  kairos reject <task>          requires --reason
  kairos answer <task>          answer any typed form: --file, --set k=v, or $EDITOR

  time travel
  kairos fork <run>             fork at a sequence, with actor/param overrides
  kairos tree <run>             parent and child runs
  kairos replay <run>           replay-verify: derived commands == recorded
  kairos events                 query the log (--correlation, --type, --since, --follow)

  definitions and inputs
  kairos flow   ls|show|graph|new
  kairos actor  ls|show|test
  kairos src    add|ls|rm|pause|poll      task sources
  kairos plugin ls|add|rm|test
  kairos secret set|ls|rm

  the engine
  kairos status                 running? queue depth, active runs, today's spend, source health
  kairos doctor                 the host probe; --live for auth/network; --explain <tool>
  kairos up | down | pause      lifecycle; up --install writes a launchd/systemd user unit
  kairos park --wait            park every run at its next node boundary, then exit — "closing the lid"
  kairos gates report           which gates are earning their place
  kairos cost                   spend by day, project, model
  kairos db backup|verify|reindex
  kairos config get|set|edit
```

**`kairos` with no arguments opens the TUI**, with two hard qualifiers: if stdout is not a terminal it
prints `status` and exits 0 (`kairos | grep` must never hang on a full-screen app), and if there is no
config yet it runs the first-run interview before creating anything.

**`Ctrl-C` detaches; it does not kill.** This is deliberate and slightly unusual, and it follows from the
daemon split:

```console
$ kairos
… (TUI) …
$ ^C
detached. engine still running: 2 runs, 1 waiting on you.
  kairos          reattach
  kairos inbox    what's waiting
  kairos down     stop the engine (asks first if runs are active)
```

Notable dropped groups: the 23 `machine`/`worker`/`pool` verbs (there is one machine and you are typing
on it — `kairos status` says what `machine describe` said, and `kairos open <run>` replaces
`worker shell` because the workspace is right there); the 12 publish/registry verbs (definitions are
files, `kairos flow ls` is a directory listing with a validation column, `diff` is `git diff`, and
versioning is git plus a content hash recorded at run start — a publish step exists to coordinate many
authors against a shared server, and there is one author); and everything auth-related.

Four verbs are **new**: `kairos open` (there is a real directory to look at, which is the best thing
about being local and deserves a verb), `kairos do`, `kairos doctor`, and `kairos park`.

---

## The TUI

Five screens, one modal boundary, and no single key that mutates anything.

### Home

```text
┌ kairos · orders-service · main ······················· ⚑2 · $1.84 · ● live ┐
│                                                                            │
│ WAITING ON YOU (2)                                                         │
│ ▸ approve   push branch + open PR · pagination      ht_01J8z   4m   ⚠ high │
│   clarify   "should cancel be idempotent?"          ht_01J8y  22m          │
│                                                                            │
│ RUNNING (3)                                                                │
│   run_01A8x  add pagination to GET /orders          ● 6/9  ·  4m12s  $0.71 │
│             └ test-and-fix · implementer · attempt 2 · 41 tools            │
│   run_01A9k  review the pagination change           ● 2/4  ·  0m38s  $0.09 │
│   run_01B2c  ↳ child of run_01A8x · write-tests     ◷ waiting: sibling     │
│                                                                            │
│ DONE TODAY (5)                                                  r → all    │
│   run_01A7m  ✓ add @bob to CODEOWNERS      2m   $0.06   PR #418           │
│   run_01A6d  ✗ bump go deps                1h   $0.31   failed at: lint   │
│                                                                            │
├────────────────────────────────────────────────────────────────────────────┤
│ ▏describe a task…                                                    ⏎ start│
├────────────────────────────────────────────────────────────────────────────┤
│ NAV  ⏎ open  j/k  a inbox  c chat  r runs  l logs  : goto  ? help          │
└────────────────────────────────────────────────────────────────────────────┘
```

- **Waiting-on-you sorts first** and is counted in the header (`⚑2`) *and* in the terminal title via
  `OSC 2`, which lights the activity flag in tmux, iTerm, WezTerm, and Ghostty. Free, and it is the
  terminal's version of a tab badge.
- **`◷ waiting: sibling` is not a spinner.** Waiting and Degraded never render as errors or spinners;
  the reason is always named — `waiting: you`, `waiting: ci`, `waiting: child`, `waiting: rate-limit`.
- **A composer on the home screen** is what makes it "a binary that starts doing work." The original
  design's home was a list; locally the home screen must accept a task, because that is the first thing
  you do and every thing you do after.
- **`$1.84` is the entire cost dashboard.** One number, today, with `kairos cost` for the report.

### Conversation

```text
┌ #pagination ······················ 3 runs · $2.90 · workspace clean · ● live┐
│ you                                                                  10:02 │
│   add cursor pagination to GET /orders. no offsets.                        │
│                                                                            │
│ implementer                                ▸run_01A8x  ✓ 4m12s  $0.71      │
│  ┌──────────────────────────────────────────────────────────────────────┐  │
│  │ 3 files · +148 −22 · services/orders/**                              │  │
│  │ gates   build ✓   test ✓ 31   lint ✓   scope ✓                        │  │
│  │ branch  kairos/pagination  (2 commits, not pushed)                    │  │
│  │ d diff    t transcript    o browser    ⏎ run                          │  │
│  └──────────────────────────────────────────────────────────────────────┘  │
│                                                                            │
│  ┌─ DECISION ─ push branch and open PR ─────────────────────── ⚑ 12m ───┐  │
│  │ ⚠ irreversible: git push, gh pr create                               │  │
│  │ ⚠ 1 open finding (medium)   ✓ gates 4/4   ◷ $2.90                     │  │
│  │                                                    ⏎ to review        │  │
│  └──────────────────────────────────────────────────────────────────────┘  │
├────────────────────────────────────────────────────────────────────────────┤
│ INPUT  @implementer ▏also add a total-count header                         │
└─ ⏎ send   ⇧⏎ newline   esc → NAV ────────────────────────────────────────┘
```

Progress **coalesces to one line with a count** (`▸ 41 tool calls`, `⇥` to expand) with milestones broken
out. That is the difference between a pane you watch and a pane you mute. A decision card is a *message*,
collapsed to its risk headline and opened in place — never a link to elsewhere.

### The keyboard model

Two modes, with a visible indicator, because a chat surface has a text field that eats single keys and
single keys must not be able to do dangerous things.

```text
NAV    single keys are commands. the default.
INPUT  single keys are text. only in a composer, a reason field, or a search box.
       enter with: i, /, ⏎ on a composer
       leave with: esc — always, unconditionally, no exceptions
```

| Key | |
| --- | --- |
| `h` `c` `r` `l` | home · conversations · runs · logs |
| `a` | inbox — jumps straight to the first waiting decision |
| `:` | resolve/goto: paste any prefixed ULID, or a screen name |
| `j`/`k` `gg` `G` `⏎` `esc` | move · top · bottom · open · back (never quits) |
| `i` | new task |
| `o` | open the focused entity in the browser |
| `y` | yank the focused id. **`y` is yank and is never yes** |
| `q` | detach — the engine keeps running |
| `Q` | quit and stop the engine — refuses with a prompt if runs are active |

**No single key mutates anything.** `x cancel`, `f fork`, and `Q` all prompt. The only unprompted
mutations are posting a message and claiming a task.

**The TUI does not execute agents**, even though it now shares a binary with the engine. The reason has
changed and this matters, because if you defend the boundary with the old argument you will lose it: the
original reason was a threat model (untrusted agent, remote machine, kernel boundary) and that reason is
gone. The reason now is **durability** — a renderer's lifetime is a terminal session, and work must
outlive it. Enforced by `TestArchitecture_tuiHasNoExecution`, which is the only thing left holding it.

---

## The approval screen

This is the load-bearing screen. Its central claim survives untouched:

> The requirement is not "show the information." It is: **make the cost of understanding lower than the
> cost of clicking through.**

One thing changes, and it changes *upward*. In the original, isolation stood between an agent and
anything valuable — a bad approval merged a PR. Here there is no sandbox at all: an approved effect runs
`git push`, `rm`, or arbitrary shell on the machine holding your SSH keys. So the local screen gains a
**required section the browser version never had**:

```text
┌ DECISION · push pagination branch and open PR ················ ⚑ open 12m ─┐
│ OBJECTIVE   add cursor pagination to GET /orders (no offsets)               │
│ ASKED BY    node push-approval · run_01A8x · implement-task@3               │
│                                                                            │
│ ┌ RISK ───────────────────────────────────────────────────────────────────┐│
│ │ ⚠ irreversible  2 effects, listed below                                 ││
│ │ ⚠ findings      1 medium (quality). 0 high.                             ││
│ │ ✓ gates         build ✓  test ✓ 31/31  lint ✓  scope ✓                   ││
│ │ ◷ cost          $2.90 of $10 limit                                      ││
│ │ ↻ attempts      `test` took 3 attempts        ← quality signal          ││
│ │ ⚠ session       1 compaction — the agent forgot part of its context     ││
│ └─────────────────────────────────────────────────────────────────────────┘│
│ ┌ HOST EFFECTS ── runs on this machine, unsandboxed ──────────────────────┐│
│ │ ⚠ git push       origin kairos/pagination   (new remote ref)            ││
│ │ ⚠ gh pr create   acme/orders-service        (irreversible: notifies)    ││
│ │ ✓ files touched  3, all inside the run's workspace                      ││
│ │ ✓ outside repo   none                       ← the line to read          ││
│ │ ✓ your checkout  ~/code/orders-service is untouched and clean           ││
│ │ ✓ commands run   go build, go test, gofmt   (no package installs)       ││
│ │ ◦ network        api.anthropic.com, proxy.golang.org, github.com        ││
│ └─────────────────────────────────────────────────────────────────────────┘│
│ ┌ CHANGED ── 3 files · +148 −22 ──────────────────────────────── d diffs ─┐│
│ │  +121 −18  services/orders/list.go                                      ││
│ │   +25  −4  services/orders/list_test.go                                 ││
│ │  declared scope: services/orders/**   ✓ no violations                   ││
│ └─────────────────────────────────────────────────────────────────────────┘│
│ ┌ FINDINGS (1) ─────────────────────────────────────────────── f expand ──┐│
│ │ medium  quality   2 TODOs added   services/orders/list.go:88,140        ││
│ └─────────────────────────────────────────────────────────────────────────┘│
├ YOUR DECISION ─── all evidence above has been rendered ────────────────────┤
│  decision   ( ) approve    ( ) request-changes    ( ) reject               │
│  reason     ▏                                                             │
│  type the decision to confirm:  ▏                        esc decide later  │
└───────────────────────────────────────────────────────────────────────────┘
```

Ordering is non-negotiable: **objective → risk → host effects → what changed → findings → decision.**
Risk before detail because risk is what changes the decision; the decision last because it should be what
you reach *after* the evidence, not what sits under your cursor.

The risk summary is **computed, never authored by a model** — the rule most likely to be violated by a
local build "because the agent already summarised it." Three lines are new and only exist because
isolation is gone: **files outside the workspace** (the most valuable line on the screen), **commands to
be executed** (with `npm ci` / `pip install` / `brew` flagged, because those are arbitrary internet code
on your host), and **your own working tree's state** — "your main is untouched" is what makes this tool
trustworthy.

### Anti-rubber-stamp mechanics

- **No single-key approve. Ever.** The confirmation is typing the decision word. Tab-completion in that
  field is disabled. A muscle-memory `y` must not be able to land an irreversible change.
- **The form is unreachable until the evidence panes have rendered.** In a terminal this is focus order,
  and it is *enforceable* rather than merely encouraged: `⇥` walks risk → host effects → changed →
  findings → decision, and the decision pane refuses focus until each prior pane has been on screen.
  There is no `tabindex` to reorder and no way to jump.
- **No global approve shortcut, and no approve action in the inbox list.** The inbox links to the
  decision screen; it never answers one. No bulk approve.
- **Evidence that failed to load blocks the form**, with a retry. The one place where partial degrades
  to blocked, and it is right.
- **Risk acceptance is a separate control**, appearing only for high/critical findings, naming the
  finding id, never bundled into the confirmation. A checkbox on every approval is trained away in a
  week.
- **The screen never claims you reviewed the diff.** It records that you saw the risk summary, the host
  effects, the scope check, and the findings. That is an honest claim, and it is what goes in the log.
- **The CLI path exists but cannot rubber-stamp**: `kairos approve <id> --confirm approve --reason "…"`,
  with no `--yes`, no `--all`, no `-f`; `--reason` required for *every* decision, not just negative ones
  (a screen can rely on evidence being visible; a pipe cannot); and a task with a high finding requires
  `--accept-risk <finding-id>`, which you cannot supply without having looked.

### Refusing to render

Below 80×24, or when risk + host effects + controls do not fit without scrolling, the screen **refuses**:

```text
│  This terminal is 68×18. The decision screen needs 80×24 to show the       │
│  risk summary, the host effects, and the controls at once.                  │
│                                                                            │
│  A cramped decision screen is how gates become theatre, so this refuses     │
│  rather than truncating.                                                    │
│    widen the terminal · o open in the browser · kairos show ht_01J8z        │
```

This is the only place in the design where a UI refuses to do its job on principle, and the principle is
correct.

### Diffs: escalate honestly, don't imitate

A 3000-line diff cannot be read on a decision screen, and pretending otherwise produces the rubber
stamp. So: **summary by path with line counts and the declared scope** by default (a scope violation
renders full-width, above the file list, in the risk colour) → `d` for an inline paged diff → **`D` hands
it to `$PAGER`**, which on the machine of anyone who cares is `delta` → `o` opens the browser for
side-by-side → `!` shells out to `git difftool` or `gh pr view --web`. The terminal's superpower is
shelling out; use it rather than imitating it.

And the primary quality evidence is the **findings**, not the diff. The reviewers read the diff; their
output is the summary.

---

## Getting told

The original design declared "not a notification system" because Slack and email were peers — getting
your attention was a transport's job. Locally there are no peers, so **the binary is the pager, because
nothing else is.** What survives of the non-goal: kairos builds no notification *centre*, digest engine,
or delivery service. It fires one deduplicated local notification and exposes state for anything else to
read.

Five layers, all of which should ship:

1. **Terminal**, when attached: `BEL` once per new task, plus `OSC 2` in the title so tmux/iTerm light
   their activity flag.
2. **Desktop notification** — the primary path. `terminal-notifier` when present, because it is the only
   one that is *actionable*: `-execute "kairos open ht_01J8z"`. Otherwise `osascript` (always available
   on macOS, not actionable, sometimes throttled) or `notify-send`. Fully overridable by a command
   template, so pointing it at `ntfy` or Pushover is one config line. **The click target is the browser**
   (`http://127.0.0.1:7717/t/ht_01J8z`), not a new terminal — spawning a terminal is fragile and lands
   you in a fresh TUI with no context, and a large diff wants the browser anyway.
3. **`kairos inbox`** — line-oriented, scriptable, and works with the daemon down by opening the store
   read-only. `--quiet` sets an exit code (`0` nothing waiting, `10` something waiting) so shell prompts
   and scripts are trivial; `--json --watch` makes every third-party integration a five-line consumer.
4. **Shell integration — the highest value per line in the whole design.** The daemon writes one integer
   to `~/.kairos/pending.count` on every queue change. `kairos shell-init zsh` emits a `precmd` hook that
   reads that file (never blocking on the daemon) and adds a right-prompt segment:
   ```text
   ~/code/orders-service main ✔                                    ⚑2 kairos
   ```
   You see it every time you press enter in any terminal, whether or not the TUI or the daemon is
   running. For someone living in a shell this beats desktop notifications on reliability and beats them
   badly on annoyance.
5. **Editor/statusbar integration: ship the mechanism, not the plugins.** No first-party VS Code
   extension. Ship `--json --watch`, the state file, and a `notify.onChange` hook, then document one
   example each for tmux `status-right`, `starship`, and `sketchybar`. Every integration becomes
   somebody else's five-line script and none of it is your maintenance burden.

**When the binary is not running at all**, be honest: nothing can page you, and no design fixes that.
Three mitigations and one refusal:

- **The daemon, not the TUI, is the persistent thing.** `kairos up --install` writes a launchd user agent
  or a systemd user unit. The window shrinks from "terminal closed" to "lid closed."
- **Nothing expires while the engine is down.** SLA and reminder timers are gated on engine uptime, so a
  laptop shut over a weekend cannot silently abandon five runs on Monday. This is a correctness
  requirement, not a nicety.
- **Graceful shutdown with open tasks writes the state file and prints the count**, so layer 4 tells you
  next time you open any shell.
- **Refused: any cloud relay, push service, or phone app** to reach you while the machine is off. That
  reintroduces an account, a network dependency, and a second threat model — the exact things the
  reduction buys its way out of. If you want to be paged with your laptop closed, you want the
  non-reduced design. The one honest middle option is **ntfy**: the daemon publishes to a topic and
  subscribes for replies over an *outbound* connection, so no inbound reachability is needed, and the
  phone app's action buttons produce a typed answer.

---

## The web page

**Go `html/template` + `//go:embed` + vendored htmx + ~200 lines of hand-written JS + plain CSS. No
Node, no npm, no lockfile, no bundler, no TypeScript.** `go build ./...` produces the whole thing.

The original chose Vite + React, and its own ADR named Go templates + htmx as *"genuinely the strongest
alternative, and the one most aligned with the project's values"*, rejecting it on three grounds and
listing four conditions for its own supersession. Every ground has vanished and every condition has
fired: the debugger that needed rich interaction now lives in the TUI; client-side virtualisation of
10,000-row timelines existed to avoid network round-trips, and on loopback a `?from=` page fetch is ~1ms;
and three of the four dashboards die with the machines and pools they described.

There is one pleasing consequence. The original's most important realtime rule was *"events invalidate,
they do not patch — patching means reimplementing the projection logic in TypeScript, and it will
diverge."* Under htmx an SSE event triggers a **server re-render of a fragment**, so the browser holds no
model to patch and the projection logic can only exist in Go, next to the engine that computed it. **The
reduced stack makes that rule structural instead of aspirational.**

Two details that remove the usual pain: `-tags dev` binds the template FS to `os.DirFS` and re-parses per
request (so editing HTML does not mean rebuilding the binary), and htmx is vendored as a checked-in file
rather than a CDN link, so a strict `default-src 'self'` CSP holds and the page works with no network.

**Localhost is not an auth mechanism.** Any web page you visit can issue requests to `127.0.0.1`, and so
can the agent you just spawned. Five cheap controls: bind loopback only (any other address refuses
without an explicit acknowledgement string in config); a per-start random token in a `0600` file,
accepted as a bearer header or a one-time `?t=` that sets an `HttpOnly; SameSite=Strict` cookie and
redirects to strip the query; a `Host` header allowlist (this is what blocks DNS rebinding, and it is the
control people forget); an `Origin`/`Sec-Fetch-Site` check on every mutating request; and a strict CSP.
About forty lines of middleware, and it removes the entire class of "a webpage in another tab started a
run."

---

## The first sixty seconds

```console
$ brew install kairos
$ cd ~/code/orders-service
$ kairos
```

```text
  kairos 0.1.0 · first run

  ✓ repo         orders-service · main · clean
  ✓ store        created ~/.kairos/kairos.db
  ✓ engine       started (pid 48120)  ·  web http://127.0.0.1:7717
  ✓ model        ANTHROPIC_API_KEY found in your environment
  ✓ workspace    clones under ~/.kairos/work/ — your checkout is never modified

  ⚠ isolation    NONE. Agents run as you, with your files. [a] to accept.
```

Zero questions when a key is in the environment; exactly one if not. No account, no telemetry prompt, no
browser opening itself. Press `a` once — recorded as an event, because the acknowledgement is a fact in
the log, not a flag — and type a task.

```text
> fix the retry backoff in the orders client, issue 421

  run_01J8QK  fix-issue                                        $0.00
  ▸ plan          claude opus      18s   $0.31   ✓ 4 tasks
  ▸ implement     claude sonnet    2m41s $0.94   ✓ 6 files
    gates         build ✓  lint ✓  no-todos ✓  coverage 84.1% ✓
    ⏸  CONFIRM  gh.pr.create → acme/orders-service       [y/n/d=diff]
```

On the **first run in a repo only**, before any of that, one dismissible screen — and this screen is the
entire trust argument for an unsandboxed local agent, at a cost of one keystroke exactly once:

```text
  I will:
    workflow    kairos/fix-issue
    actor       implementer  (claude-sonnet)
    workspace   ~/.kairos/work/run_01J8QK/repo   (clone, branch kairos/run_01J8QK)
    scope       services/orders/**    (files outside will be flagged)
    budget      $10, 30 min           then it stops and asks
    gates       build, test, lint  ·  approval before push

    ~/code/orders-service is not touched.

  ⏎ start    e edit    esc cancel    d don't ask again
```

Nothing was configured. No YAML was written. The workflow was a built-in, the gates were specialised from
the detected `go.mod`, `gh` auth came from the host keychain, and the budget was printed before the first
token was spent.

**One requirement this implies that is easy to miss: the binary must ship built-in workflows and
actors.** YAML-first is right as the *extension* path, but if the first sixty seconds require authoring a
workflow definition, the product is dead on arrival. Ship `fix-issue`, `implement`, `review`, and
`answer-question` compiled in under a reserved namespace, listable with `kairos flow ls` and ejectable to
YAML with `kairos flow eject fix-issue > .kairos/fix-issue.yaml`.
