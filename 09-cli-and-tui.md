# 09 — The CLI and the TUI

**Two co-equal surfaces.** Anything you can do in the terminal you can do in the browser, and the
reverse. The web UI has its own document — [`10-webui.md`](10-webui.md); this one is the terminal and the
command line.

Parity is a requirement, not an aspiration, and it is cheap to hold because of a decision made in
[`01-architecture.md`](01-architecture.md): **both surfaces are clients of the same in-process API over
the same unix socket, and neither holds state.** A capability that exists in one and not the other is a
missing handler, not a missing architecture.

| | **TUI** (`kairos`) | **Web** (`127.0.0.1:7717`) |
| --- | --- | --- |
| start work, chat, approve, cancel, fork, follow logs, inspect | ✓ | ✓ |
| better at | latency; no context switch; approving without leaving the shell; shelling out to `$PAGER`, `git difftool`, `gh`; working over SSH; a right-prompt badge | review-quality diffs; long transcripts with search; side-by-side comparison; 10k-event timelines; a pasteable URL |
| realtime | SSE over the unix socket | SSE over loopback — the same stream, the same `Last-Event-ID` |
| writes | the API | the API |

The ancestor design split its browser surfaces into a conversational Console and an operational
Operations UI, on the grounds that they were two jobs for two moods. That split dies: one person, one
origin, one app. What replaces it is not a *narrower* web surface but a **complete** one.

The component that must be identical in both is the decision card — a 3000-line diff approval wants a
browser, a `git push` approval wants to be answered without leaving the terminal — and both render from
the same server-computed decision context, so the evidence cannot drift even where the presentation does.

**The anti-rubber-stamp rules are surface-independent.** Evidence before controls in focus order, no
single-key or single-click approve, the typed decision word, a blocked form when evidence failed to load.
Parity means parity on the constraints too; a browser that is easier to rubber-stamp in than the terminal
would make the gate worthless in exactly the place people would use it most.

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
  kairos runner add|ls|rm|probe|drain     execution targets — see 07-runners.md

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

Five verbs are **new**: `kairos open` (there is a real directory to look at, which is the best thing
about being local and deserves a verb), `kairos do`, `kairos doctor`, `kairos park`, and `kairos runner`.

### Global flags, and the rule about them

```text
-o, --output  table | json | yaml | wide      default table; json is stable, table is not
-q, --quiet   ids only, one per line          for xargs
-w, --follow  stream until terminal or Ctrl-C
    --wait    block until the run reaches a terminal state; exit code reflects the outcome
    --json    alias for -o json
```

Two rules worth writing down because they decide whether this is scriptable:

- **`-o json` is a contract; the table is not.** Table columns may be reordered or renamed freely. JSON
  field names are append-only, exactly like event payloads. Anything else means every script anyone
  writes breaks on a patch release.
- **There is no `--yes` that answers a decision.** `--wait` and `-q` exist so a script can *drive* work
  and *observe* outcomes; approving is deliberately not scriptable past the constraints in
  [`05-gates.md`](05-gates.md).

### Exit codes

Scripting needs these stable, and a single number that means "something went wrong" makes a wrapper
impossible to write:

| | |
| --- | --- |
| `0` | success — and for `--wait`, the run succeeded |
| `1` | an error |
| `2` | usage error |
| `3` | not found |
| `4` | validation failed (a definition, a form answer, a config field) |
| `5` | denied by policy |
| `7` | the run reached a non-success terminal state (failed, cancelled, degraded) |
| `8` | an invariant violation — a bug, and it says so and tells you to file it |
| `10` | `kairos inbox --quiet`: something is waiting on you |

`10` is deliberately outside the error range: shell prompts call `inbox --quiet` on every render, and a
"waiting" signal must not look like a failure to `set -e`.

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

### Run inspector

`r` on a run, or `:` and paste any `run_…` id. This is where the run timeline and the node detail merged,
because with one machine and one workspace there is not enough per-node content to justify two screens.

```text
┌ run_01A8x · implement-task@3 · ✓ succeeded · 4m12s · $0.71 ················┐
│ objective   add cursor pagination to GET /orders (no offsets)              │
│ workspace   ~/.kairos/work/run_01A8x/repo   clone · clean · 148 MB   [o]   │
│ runner      local                        parent —   children run_01B2c ✓   │
│                                                                            │
│ TIMELINE                                                        t n a e     │
│  ●  00:00.0  run.started                                                   │
│  ✓  00:00.4  plan             rule           0.4s      —                   │
│  ✓  00:31.2  read-context     implementer   30.8s   $0.09   12 tools       │
│  ✓  02:04.7  implement        implementer   93.5s   $0.38   41 tools   ▸   │
│  ✗  02:41.0  test             shell         36.3s      —    exit 1         │
│  ↻  02:41.0  test-and-fix     implementer   attempt 2 of 3                 │
│  ✓  03:58.9  test             shell         77.9s      —    exit 0         │
│  ⚑  04:02.1  push-approval    human         waiting 12m              ⏎     │
│  ✓  04:12.0  run.succeeded                                                 │
│                                                                            │
│ EFFECTS (2)                                                                │
│   git.commit   2 commits on kairos/pagination            reversible        │
│   git.push     origin kairos/pagination                  ⚠ irreversible    │
│                                                                            │
│ FINDINGS (1)   medium  quality   2 TODOs added   list.go:88                │
│ SESSION        1 compaction ⚠      3 attempts on `test`                    │
│ ARTIFACTS      diff (12 KB) · transcript (840 KB) · coverage (4 KB)   [a]  │
├────────────────────────────────────────────────────────────────────────────┤
│ NAV ⏎ node  l logs  d diff  f fork  x cancel  B breakpoint  o browser  esc │
└────────────────────────────────────────────────────────────────────────────┘
```

Filters on the timeline: `t` transitions only, `n` node executions, `a` attempts expanded, `e` effects.
`⏎` on a node expands **in place** — input, output, findings, the transcript head — rather than opening a
screen; full transcripts go to the browser or `$PAGER`.

Two rows earn their place on the primary view rather than behind an expander, because they are the
highest-value under-surfaced signals in the system: **`↻ attempt 2 of 3`** and **`1 compaction ⚠`**.
"This node took four attempts and compacted twice" is exactly when to look harder, and both are free to
compute.

`f fork` and `B breakpoint` live here. That is the decision that makes the web page read-mostly: **the
debugger ships in the terminal**, so the browser needs no rich interaction, so htmx is the right stack
for it. Those three facts are one decision, not three.

### Log follow

```text
┌ logs · run_01A8x · node test · stdout+stderr ········· ⏸ 8,412 lines ─────┐
│ 03:57.2 │ go test ./services/orders/...                                   │
│ 03:58.1 │ ok    services/orders/repo        0.412s                        │
│ 03:58.4 │ --- FAIL: TestListCursor (0.01s)                                │
│ 03:58.4 │     list_test.go:88: expected 20 items, got 21                  │
│ 03:58.4 │ FAIL  services/orders            1.104s                         │
│         │                                                                 │
│ ┄┄┄ paused · 12 new lines below · G to follow ┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄ │
├──────────────────────────────────────────────────────────────────────────┤
│ NAV  G follow  gg top  / search  n/N  w wrap  1 stdout  2 stderr  3 both  │
│      o $PAGER  a write artifact to a path  esc                            │
└──────────────────────────────────────────────────────────────────────────┘
```

A **capped tail, not a log viewer**: the last 10,000 lines in a ring, `a` writes the full artifact to a
path and prints it, `o` hands the file to `$PAGER` and gets out of the way. Locally this is *better* than
any viewer that could be built — the log is a file on your disk and `rg` is right there.

**No autoscroll while you are scrolled up**, and the divider says how many lines you are behind. That
divider is the terminal's "jump to end", and omitting it is how a follow view becomes unusable.

### Inbox

`a` from anywhere. The queue of things waiting on **you**, which is the screen the header badge points at.

```text
┌ inbox · 2 waiting ····························································┐
│ ▸ ⚠ approve   push branch + open PR · pagination                            │
│       run_01A8x · asked 12m ago · 1 medium finding · $2.90                   │
│   · clarify   "should cancel be idempotent?"                                │
│       run_01A6d · asked 31m ago                                             │
│                                                                             │
│   nothing else is blocked on you. 3 runs are working.                       │
├─────────────────────────────────────────────────────────────────────────────┤
│ NAV  ⏎ open the decision   j/k   r refresh   esc                            │
└─────────────────────────────────────────────────────────────────────────────┘
```

**This list never answers a decision.** It links to the decision screen and nothing else — no inline
approve, no bulk approve, no keyboard shortcut that resolves an item from here. That is the same rule as
the missing global approve key, and it exists for the same reason.

### Runners

Only interesting once you have more than one; see [`07-runners.md`](07-runners.md). With just `local` it
is one line, and that is correct — a screen that shows one row is honest about there being one row.

```text
┌ runners ······································································┐
│ NAME      KIND   HEALTH     RUNS  CPU-HEAVY  DISK FREE  TOOLCHAIN           │
│ local     local  ✓ healthy   3/4      2/2      412 GB   go 1.25 · node 22   │
│ beelink   ssh    ✓ healthy   1/4      0/2       94 GB   go 1.25 · no node ⚠ │
│ macmini   ssh    ◌ probing      —        —           —  —                    │
│                                                                             │
│ model slots are GLOBAL, not per runner: opus 2/2 · sonnet 1/3                │
├─────────────────────────────────────────────────────────────────────────────┤
│ NAV  ⏎ detail  p probe  D drain  esc                                        │
└─────────────────────────────────────────────────────────────────────────────┘
```

The `no node ⚠` and the global-slots line are the two things this screen exists to tell you: which
workflows *cannot* run where, and why adding a machine did not double your throughput.

### Benchmark

`b`. Fork one run N ways from a common prefix, vary one thing, and show the spread.

```text
┌ bench · run_01A8x @ seq 14 · vary: actor.implement ·······················┐
│ VARIANT          RUNS  PASS  p50 COST   p50 TIME   FINDINGS  GATES          │
│ claude-sonnet       3    3/3     $0.71    ~4m12s        1.0    12/12         │
│ claude-opus         3    3/3     $2.90    ~3m48s        0.3    12/12         │
│ codex               3    2/3     $0.44    ~6m01s        2.7    11/12         │
│                                                                             │
│ ~ durations are not comparable: variants ran concurrently and contended     │
│   for CPU and your rate limit. cost, gates, and findings ARE comparable.     │
│ 3 repeats is an anecdote, not a result.                                     │
└─────────────────────────────────────────────────────────────────────────────┘
```

Both caveats are printed, always. The wall-clock one is new and local-specific — concurrent variants
contend for the same machine and the same subscription — and silently reporting contaminated durations
would be worse than the honest note. Run variants serially with `--serial` if you want the timing.

### The command palette

`:` — the replacement for a cross-surface `⌘K`, and deliberately small. Paste any prefixed ULID
(`run_`, `ht_`, `nex_`, `src_`) and it resolves to the right screen; type a screen name; type a verb.
No fuzzy-search over everything, no command history as a feature.

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

### Navigation

```text
                 ┌──────── : (resolve any id) ────────┐
                 ▼                                    ▼
   ┌──────► HOME ──── c ────► CONVERSATION ──⏎──► DECISION
   │         │  │                  │  ⏎               │
   h         a  r                  ▼                 esc
   │         │  │           RUN INSPECTOR ──l──► LOGS
   └─────────┘  └──────r───────────┘  ▲               │
                                      └─────esc───────┘

esc walks back up this graph and never exits the program.
o at any node opens the same entity in the browser.
```

One level deep everywhere: nothing is more than two `esc` from home. The five-second question — *can you
tell what this screen is for in five seconds* — has to hold per screen, and a short descent is what keeps
it answerable.

### The status bar

Three zones, and every element is load-bearing rather than decoration:

```text
┌ kairos · orders-service · main ······················· ⚑2 · $1.84 · ● live ┐
  └──┬───┘   └──────┬──────┘  └─┬─┘                       └┬┘   └──┬─┘  └─┬─┘
   app        project/repo    branch              waiting-on-you  today  link
├────────────────────────────────────────────────────────────────────────────┤
│ NAV  ⏎ open  j/k  a inbox  c chat  r runs  l logs  : goto  ? help          │
  └─┬─┘ └──────────────────────┬───────────────────────────────────┘
   mode          context-sensitive bindings, never a fixed list
```

- **`⚑2` is mirrored into the terminal title** via `OSC 2`, which lights the activity flag in tmux, iTerm,
  WezTerm, and Ghostty. Free, and it is the only way a background pane can get your attention.
- **`● live` / `◐ 45s` / `◌ engine not responding`** is the quiet-versus-disconnected distinction. It
  matters *more* here than it would remotely: a dead daemon means nothing is executing at all, and a
  screen that looks merely idle when the engine is gone is the worst possible lie for this tool to tell.
- **`$1.84` is the entire cost dashboard.** One number, today. `kairos cost` is the report.
- **Bindings are context-sensitive and never a fixed list.** A key that does nothing on this screen is not
  shown on this screen.

### How live updates arrive

SSE over the unix socket, resumable by `Last-Event-ID` — the *same* mechanism the web page and
`--follow` use. Keeping one resumption story rather than three is worth more than the microseconds a
private channel would save.

Each event becomes one message in the update loop, and the loop **invalidates and refetches** rather than
patching, with conversation-message append as the single exception. That rule matters more locally, not
less: a projection reimplemented in the renderer would diverge from the engine's projection *inside the
same process*, which is a genuinely humiliating class of bug.

Two mechanical rules: **progress coalesces at the renderer, not at the source** (every tool call is in
the log; the pane shows a count), and **redraw is diffed and capped at ~10 Hz** — not for bandwidth, but
because a full repaint fights tmux, breaks text selection, and burns battery.

Reconnect collapses to something a human can read: retry every 500 ms, say `◌` after 3 s, and after 10 s
say `◌ engine not responding — kairos serve`. The jittered exponential ladder a network needs is noise on
a socket.

### Screen states

Six, and the two rules about them are the ones most often broken:

| State | Rendering |
| --- | --- |
| loading (first paint) | a skeleton, never a spinner over an empty frame |
| loading (refresh) | invisible — the old content stays until the new arrives |
| empty | says what *would* create one. Never a bare "no results" |
| partial | shows what loaded, names what did not, offers retry |
| error | the message, the correlation id, and `kairos events --correlation <id>` to run yourself |
| stale | the engine is unreachable; content is dimmed and dated, never silently frozen |

1. **`Waiting` and `Degraded` are not errors and not spinners.** They render with the reason named —
   `waiting: you`, `waiting: ci`, `waiting: child`, `waiting: rate-limit`. A run parked on a human for
   three days is the system working correctly and must not look like a hang.
2. **Unknown is `—`, never `0`.** A cost that has not been reported is not free, and rendering it as
   `$0.00` produces a confident wrong number in the one place people trust numbers.

### Accessibility, in a terminal

Small list, all cheap, none optional: honour `NO_COLOR` and `TERM=dumb`; keep the palette 8-colour-safe
and **never** carry severity by colour alone; and treat **`--no-tui` as a first-class, permanently
supported surface** — the line-oriented `kairos show` / `kairos logs --follow` path. A full-screen TUI is
genuinely hostile to a screen reader, so the line-oriented mode is *the accessible surface*, not a
degradation of one.

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

## Where the browser takes over

`o` on any focused entity opens the same thing at `127.0.0.1:7717`. The full specification is
[`10-webui.md`](10-webui.md); the boundary is what matters here.

The terminal hands off for exactly five things, and the reason is the same each time — a terminal is a
one-column device and these are not one-column problems:

| | Why the terminal loses |
| --- | --- |
| **diffs at review quality** | syntax highlighting, side-by-side, word-level intra-line, 62 files |
| **long transcripts with search** | a 40-turn session with tool calls is a *document* |
| **side-by-side comparison** | two forks, two runs, a benchmark table with its diffs |
| **a pasteable URL** | into a ticket, a commit message, a note to yourself |
| **very wide reads** | a 10,000-event timeline, a coordinator's causal tree with twelve children |

And the dividing test, so the seam can be checked rather than argued about: **if you find yourself doing
something in the browser more than a few times an hour, it belongs in the TUI.** Starting work, chatting,
approving small things, and tailing logs are the terminal's; any of them appearing in the browser is the
signal that the boundary has drifted.

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
