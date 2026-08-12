# 02 — Configuration

One file. Every field has a default. You can delete the whole file and the binary still works.

```text
$KAIROS_HOME              default ~/.kairos  (respects XDG_STATE_HOME)
├── config.yaml           this document
├── kairos.db             SQLite: events, projections, cursors, dedupe
├── logs.db               log chunks — deleting this must never break a replay
├── daemon.sock           0600, HTTP/1.1 over a unix socket
├── daemon.lock           flock target, so two engines can never race
├── pending.count         one integer; the shell-prompt fast path
├── flows/*.yaml          workflow definitions — the filesystem IS the registry
├── actors/*.yaml         actor definitions (overrides only; builtins are compiled in)
├── plugins/<name>/       plugin.json + an executable
├── inbox/                drop a .md file here and it becomes a task
├── mirrors/<host>/<repo>.git    bare mirrors, gc.auto=0; runs clone --reference from these
├── work/<runID>/         the run's clone + its per-run HOME
├── artifacts/<sha256>/   content-addressed
└── secrets.json          0600, or delegated to the OS keychain
```

**Why a `--reference` clone off a kairos-owned mirror.** Three properties have to hold at once: cheap
(a child run must not cost a full network clone), isolated (two concurrent runs must not collide), and
never touching your own checkout. `git clone --reference <mirror>` gets all three — objects are
borrowed through `objects/info/alternates`, while refs, index, and config are private per run.

`git worktree` is cheaper and fails the middle requirement: worktrees share the mirror's ref namespace
and config, so two runs collide in `refs/heads/`, an agent's `git rebase` or `git config` reaches
outside its own workspace, and a mirror `fetch --prune` can delete a ref a live run is standing on.

The sharp edge of `--reference`, which must be handled or it bites in month two: `git gc` in the
mirror can repack away objects a borrower depends on, producing "object not found" inside a live
workspace. So every mirror is created with `gc.auto=0` and `gc.pruneExpire=never`, and maintenance
runs only when the event log says no non-terminal run references that mirror. `--dissociate` is
available per repo for anyone who wants full independence at the cost of a real object copy.

**A workspace is never your own checkout.** It is always a clone under `~/.kairos/work/`. The moment
an autonomous agent with `--permission-mode acceptEdits` is pointed at your actual working tree, this
tool has become the thing it exists to avoid.

---

## config.yaml, complete

```yaml
# ─── how much of your machine Kairos may use ─────────────────────────────
admission:
  nodes: 4                # concurrent node executions      default min(4, NumCPU/2)
  pools:
    cpu.heavy: 2          # concurrent build/test/lint       default max(1, NumCPU/4)
  maxOpenDecisions: 5     # stop starting work when 5 things already wait on you
  maxQueued: 40           # beyond this, REJECT rather than queue — silent truncation lies

# ─── who does the work ───────────────────────────────────────────────────
models:
  strong:   { cli: claude, args: [--model, opus],          slots: 2 }
  balanced: { cli: claude, args: [--model, sonnet],        slots: 4 }
  cheap:    { cli: codex,  args: [--model, gpt-5-codex],   slots: 4 }
  local:    { endpoint: "http://127.0.0.1:11434", model: qwen2.5-coder:7b, slots: 1 }

# ─── money and time ──────────────────────────────────────────────────────
limits:
  wallClock: 2h
  maxCostUSD: 10          # per run
  dailyUSD: 25            # across all runs; exceeding creates a decision, never a silent stop
  ceiling: 50             # a workflow may not raise maxCostUSD above this

# ─── how child processes are launched ────────────────────────────────────
exec:
  niceness: 10            # children are nice'd; your laptop stays usable
  sandbox: auto           # auto | off  — sandbox-exec (macOS) / bwrap+landlock (Linux)
  killGrace: 10s          # SIGTERM → wait → SIGKILL, on the process group

# ─── what needs your approval ────────────────────────────────────────────
approvals:
  push: true              # git push, open/merge a PR
  externalComment: true   # comment on issues, move tickets
  overBudget: 5.00        # any run about to exceed this
  fileEdits: false        # editing files inside the worktree
  buildsAndTests: false   # running builds and tests

# ─── how you get told ────────────────────────────────────────────────────
notify:
  command: ["terminal-notifier", "-title", "Kairos",
            "-message", "{{.Title}}", "-execute", "kairos open {{.ID}}"]
  on: [task.opened, task.reminder, run.failed]
  quietHours: "23:00-08:00"    # reminders defer; new high-risk decisions do not

# ─── your repos ──────────────────────────────────────────────────────────
projects:
  orders:
    path: ~/code/orders-service
    constitution: ~/.kairos/projects/orders/constitution.yaml
    defaultFlow: fix-task
    tasksources:
      - kind: github-issues
        repo: acme/orders
        filter: { labels: [kairos], state: open }
        every: 2m
      - kind: file-inbox
        path: ~/.kairos/inbox
```

`kairos config show` prints the effective merge with every default it filled in and where each value
came from. `kairos check` validates it and the host in one pass.

---

## The defaults, and why each one is that number

| Setting | Default | Reasoning |
| --- | --- | --- |
| workspace mode | `worktree` off a kairos-owned mirror | The local substitute for isolation-of-*changes*. Your checkout is untouched, concurrent runs cannot collide, and integration is a branch merge with nothing to fetch. |
| snapshots | `clonefile(2)` on APFS, `cp --reflink=auto` on btrfs/XFS, plain copy elsewhere | Keeps fork/replay cheap without ZFS. On a copy-fallback filesystem the binary **advertises `snapshot: slow` and prints the real duration** rather than pretending it was instant. |
| `admission.nodes` | `min(4, NumCPU/2)` | The binding limit is rarely CPU — see below. |
| `cpu.heavy` | `max(1, NumCPU/4)` | Two concurrent `go test ./...` on 8 cores is *slower* than running them serially, and it thrashes. |
| model slots | `strong: 2` | Leaves your own `claude` usable while Kairos works, and stays under subscription concurrency. |
| `dailyUSD` | 25 | It is your card. A cap that creates a decision is better than one that kills a run at 90%. |
| poll interval | **120s**, jittered, backoff to 30m | You share GitHub's rate limit with your own `gh`. A 30s poller means *your* `git push` starts failing. Prefer `GET /notifications` with `If-None-Match` — a 304 costs no quota. |
| `cron` catch-up | `skip` | A laptop closed for 14 hours is Tuesday, not an incident. Waking to six nightly runs firing at once is how a tool gets uninstalled. |
| `onTimeout` | `park` (with `sla: 7d`) | On a fleet, a parked run leaked a workspace and a queue slot, so `abandon` was safe. Locally a parked run costs one directory. Auto-failing five runs because you took a holiday is strictly worse. |
| retention | events forever · worktrees 7d after run end · artifacts 30d · dedupe keys 30d | |
| SQLite | `journal_mode=WAL`, `synchronous=FULL`, `busy_timeout=5000` | `FULL` costs ~1ms per append at a few hundred events/minute and buys the thing that matters: an `effect.applied` event is durable before the process can die. Losing it puts a PR in `Unknown` and forces a probe. |

### The three scarce resources, kept separate

This is the part naive local designs get wrong. They are not the same resource and must not share a
counter:

| Resource | Consumed by | The real cap |
| --- | --- | --- |
| **Model concurrency** | agent CLIs waiting on the API | your rate limit and your spend — *usually the binding one* |
| **CPU / RAM** | the agents' *tool calls*: `go build`, `npm test`, `cargo check` | `cpu.heavy` |
| **Workspace disk** | one worktree per run and per child | retention + `refuseAt` free-space floor |

An agent CLI is **network-bound, not CPU-bound** — three of them sit idle waiting on the API. So four
child runs can all be live, agents thinking, worktrees checked out, while only two are compiling.
That decoupling is what makes local fan-out feel fast instead of feeling like a fork bomb.

---

## Admission: what replaces the scheduler

There is nothing to *place*, only to permit. One function, checked when a run is created and each
time a node execution finishes. First failure wins, and the reason is shown verbatim in `kairos ls`:

```text
1. engine draining                     → "shutting down"
2. running execs >= admission.nodes    → "4 of 4 slots busy"
3. workspace write-locked by another run→ "orders busy: run_01K4Y is writing"
4. model slots exhausted               → "2 of 2 claude processes busy"
5. spend + estimate > dailyUSD         → "$24.10 of $25.00 spent today"
6. open decisions >= maxOpenDecisions  → "5 decisions already waiting on you"
7. queued >= maxQueued                 → REJECT (not queue), reported back to the task source
```

Rules 3 and 6 are the interesting ones. **Rule 3 — one writer per workspace — is what the entire
distributed scheduler's affinity machinery was approximating.** Locally it is a `flock`. **Rule 6 is
backpressure on your attention**: don't create a sixth decision before I've answered some of the
five. Rule 7 keeps explicit rejection because a misconfigured integration firing 500 events must
produce 500 *visible* rejections — silent truncation reads as "the system ignored me."

A run that is `Waiting` (on you, on CI, on a child) **holds none of these.** It releases its
execution slot, its model slot, and its worktree write lock. It keeps only the directory.

---

## Secrets, and the one rule that matters

Secrets live in `secrets.json` (0600) or the OS keychain, and are **brokered, never exported**.

The agent's environment contains `PATH`, `HOME`, `KAIROS_*`, the model API key, and nothing else. No
`GH_TOKEN`, no `GITHUB_TOKEN`, no `JIRA_*`, no `AWS_*`. On top of that, the run's worktree is
configured:

```bash
git config --local credential.helper ""
git config --local core.askPass /usr/bin/false
git config --local core.hooksPath /dev/null
git config --local remote.origin.pushurl "kairos-blocked://denied"
```

So an agent typing `git push` fails immediately regardless of prompts, tool allowlists, or how
clever it is being — **there is no credential and no push URL.** Pushing happens only through the
engine's own `builtin.git-push` node, which runs in a different process with a different environment
populated from the keychain *after* the policy check.

That is the difference between a permission and a capability, and it is the single most effective
control available on a machine with no isolation. See [`09-limitations.md`](09-limitations.md) for
what it does not cover.
