# Kairos

**One binary. One machine. It reads your triggers, launches agent CLIs on the task, and runs them
through a durable workflow with gates you cannot skip.**

No infrastructure: no VMs, no containers, no Kubernetes, no remote nodes, no Postgres, no gRPC. The
host machine *is* the worker, and the host's installed tooling *is* the image.

> **Status: design, not code.** Nothing here is implemented yet. These eleven documents are written so
> that a coding agent or a human can pick up one of them, implement it in isolation, and know how it
> fits. [`10-build-plan.md`](10-build-plan.md) is the order to build them in.
>
> Read this document and you have the whole idea. The other ten are the detail.

---

## The loop

Everything the binary does is one loop, and every arrow is an event appended to a log before
anything happens.

```text
  TRIGGERS
  ┌──────────────────────────────────────────────────────────────┐
  │  tasksource poll   github issues · jira · linear · file inbox │
  │  kairos do "…"     you publish a task yourself                │
  │  kairos chat       a message in the TUI                        │
  │  kairos run f.yaml a workflow, by hand                         │
  └───────────────────────────┬──────────────────────────────────┘
                              │  append trigger.received
                              ▼
                    ┌──────────────────┐
                    │  RUN of a        │   durable: the run IS rows in SQLite
                    │  workflow        │   Ctrl-C and restart resumes it
                    └────────┬─────────┘
                             │ admission: a concurrency slot + a model slot
                             ▼
    ┌────────────────────────────────────────────────────────────────┐
    │ node: plan          claude -p … --output-format stream-json     │
    │                     cwd = ~/.kairos/work/<run>/repo             │
    │                     typed JSON out, JSON-Schema validated       │
    ├────────────────────────────────────────────────────────────────┤
    │ GATE                go build · golangci-lint · jsonpath assert  │
    │                     ← run by the ENGINE, after the agent exited │
    │                       the agent cannot see, wrap, or skip it    │
    ├────────────────────────────────────────────────────────────────┤
    │ node: implement     another agent CLI, same workspace, resumed  │
    │ node: review        2–3 agent CLIs in parallel, different CLIs  │
    │ node: ci-watch      polls `gh run list`, holds nothing          │
    │ node: approve       waits for you, holds nothing, survives      │
    │                     restart, pokes you via desktop notification │
    ├────────────────────────────────────────────────────────────────┤
    │ node: pr            gh pr create ← run by the engine, not the   │
    │                     agent; declared effect; compensable         │
    └────────────────────────────────────────────────────────────────┘
                             │
                             ▼
                    run.succeeded · workspace kept 1h · artifacts kept
```

Several runs execute at once, each in its own workspace directory, bounded by slot counts so you
do not blow your model rate limits or thrash the machine.

---

## What survives, what dies

The reduction is not "the same system, smaller". Two thirds of the original corpus existed to make
work *portable across machines*. That third dies outright. Everything that existed to make work
*durable, typed, and gated* survives, and several parts get **better** locally.

| Survives untouched | Survives, reshaped | Dies |
| --- | --- | --- |
| Event sourcing; append-only log; state is a projection | Event store: Postgres → **SQLite (WAL)** | Firecracker, Docker, Kubernetes, SSH runtimes |
| Durable runs; restart-safe; week-long waits | Runtime abstraction → **one executor package** | The machine agent, the Machine Protocol, gRPC, mTLS |
| Typed node contracts (JSON Schema out, mandatory) | Scheduler → **in-process admission control** | Placement, affinity, labels, taints, spreading, preemption |
| Gates run by the engine, not the agent | Workspaces → **`git clone --reference` off a local mirror** | Worker leases, warm pools, VM snapshots, `holdThreshold` |
| Constitution / mandatory gates, non-removable | Policy → **three-tier effect permissions** | RBAC principals, teams, roles, `policy simulate` |
| Effects + compensations; `effect.attempted` first | Human queue → **one user, TUI + notification** | SLAs, escalation-to-team, queue routing |
| Replay and fork of a run | Sessions → **the agent CLI's own `--resume`** | Egress allowlists, kernel isolation, image pinning |
| Child runs, coordinator fan-out, `Degraded` | Transports → **TUI + localhost console** | Multi-machine growth path (foreclosed, not deferred) |

### The two things genuinely lost

**1. Isolation.** The original design's answer to a prompt-injected agent was *"it compromised a
disposable microVM"*. There is no VM. An agent runs as you, on your machine, with your files. See
[`09-limitations.md`](09-limitations.md) — this is stated plainly, not buried, and the mitigations
that remain are real but weaker.

**2. The twenty-machine path.** Law L13 of the original corpus promised *"useful on one machine,
unchanged on twenty"*. Kairos Local trades that away for zero setup. Re-adding it later is a
rewrite of admission, workspace ownership, and the event bus — not a configuration change.

---

## The first sixty seconds

```console
$ brew install kairos          # or: go install; or download one binary
$ cd ~/src/acme-backend
$ kairos
```

First run, no config file, nothing installed:

```text
┌─ kairos ──────────────────────────────────────────────────────────────┐
│  First run. Setting up ~/.kairos …                          ✓         │
│                                                                       │
│  Host probe                                                           │
│    git 2.51        ✓    claude 2.1.4     ✓    gh 2.63 (authed)  ✓    │
│    go 1.25         ✓    golangci-lint    ✓    codex 0.9        ✓    │
│    filesystem      APFS — copy-on-write clones available               │
│    isolation       NONE. Agents run as you, with your files.          │
│                                                                       │
│  Project detected: acme-backend  (github.com/acme/backend, Go)        │
│    gates specialised:  build → go build ./...                         │
│                        lint  → golangci-lint run --new-from-rev       │
│                        test  → go test ./... -coverprofile            │
│                                                                       │
│  Type a task, or press [t] to connect a task source.                  │
│  ⚠ Agents will run commands on this machine as you. [a] to accept.   │
└───────────────────────────────────────────────────────────────────────┘
```

Press `a` once (recorded as an event — the acknowledgement is a fact in the log, not a flag), then
type a task:

```text
> fix the retry backoff in the orders client, issue 421

  run_01J8QK  fix-issue                                        $0.00
  ▸ plan          claude opus      18s   $0.31   ✓ 4 tasks
  ▸ implement     claude sonnet    2m41s $0.94   ✓ 6 files
    gates         build ✓  lint ✓  no-todos ✓  coverage 84.1% ✓
    ⏸  CONFIRM  gh.pr.create → acme/backend            [y/n/d=diff]
```

Nothing was configured. No YAML was written. The workflow was the built-in `fix-issue`, the gates
were specialised from the detected `go.mod`, `gh` auth came from the host keyring, and the model
budget came from a shipped default that was printed before the first token was spent.

---

## Configuring it

One file, and every field has a default. This is the whole surface for normal use.

```yaml
# ~/.kairos/config.yaml
admission:
  nodes: 4                      # concurrent node executions   default min(4, NumCPU/2)
  pools:
    cpu.heavy: 2                # concurrent build/test/lint    default max(1, NumCPU/4)

models:
  strong:   { cli: claude, args: [--model, opus],   slots: 2 }
  balanced: { cli: claude, args: [--model, sonnet], slots: 4 }
  cheap:    { cli: codex,  args: [--model, gpt-5-codex], slots: 4 }
  local:    { endpoint: "http://127.0.0.1:11434", model: qwen2.5:3b, slots: 1 }

limits:
  wallClock: 2h
  maxCostUSD: 10                # per run
  ceiling: 50                   # a workflow may not raise maxCostUSD above this

exec:
  niceness: 10                  # children are nice'd; the laptop stays usable
  sandbox: auto                 # auto | off — sandbox-exec (macOS) / bwrap+landlock (Linux)

projects:
  acme-backend:
    repo: github.com/acme/backend
    constitution: ~/.kairos/projects/acme-backend/constitution.yaml
    defaultWorkflow: feature-delivery
    tasksources:
      - kind: github-issues
        repo: acme/backend
        filter: { labels: [kairos], state: open }
        every: 2m
      - kind: file-inbox        # drop a markdown file in a directory, it becomes a task
        path: ~/kairos-inbox
```

`kairos doctor` prints the effective merge, the host probe, and every default it filled in.

---

## Triggers: how work arrives

Four ways in, one code path out. Each produces a `trigger.received` event and a Run. There is no
"ad-hoc mode" that bypasses the log — that is the law the original corpus calls L9 and it is the
single most important one to not break while building this.

| Trigger | Mechanism | Notes |
| --- | --- | --- |
| **Task source poll** | a ticker in-process; `gh issue list --json …` etc. | Polling, not webhooks: a laptop has no public URL. Cursor + dedup key stored in SQLite. |
| **`kairos do "…"`** | you publish a task directly | Runs the built-in `adhoc` workflow: classify → implement → gate → confirm. |
| **`kairos chat`** | a message in the TUI | A conversation spanning many runs. Follow-ups resume the same agent session. |
| **`kairos run f.yaml`** | a named workflow, by hand | The scripting path; what CI-like use looks like. |
| *(optional)* webhook | `gh webhook forward` into `127.0.0.1:7777` | Opt-in. Poll is the default and always works. |

A task source is a **plugin**: an executable in `~/.kairos/plugins/` speaking JSON on stdout. Four
ship compiled in (github-issues, jira, linear, file-inbox), so you write nothing for the common
cases. See [`08-tasksources.md`](08-tasksources.md).

---

## Running several agents on one task

This is the part people expect not to work locally. It does, because **an agent CLI is
network-bound, not CPU-bound** — three of them sit idle waiting on the API. The three scarce
resources are separate and modelled separately:

| Resource | Consumed by | The real cap |
| --- | --- | --- |
| Model concurrency | agent CLIs waiting on the API | your rate limit and your spend |
| CPU / RAM | the agents' *tool calls* — `go build`, `npm test` | `cpu.heavy` semaphore |
| Workspace disk | one worktree per run/child | `keepWorktrees` retention |

So four child runs can all be "live" — agents thinking, worktrees checked out — while only two are
compiling. That decoupling is what makes local fan-out feel fast instead of feeling like a fork
bomb. On an 8-core/32 GB machine: `nodes: 4`, `cpu.heavy: 2`, `strong.slots: 2` runs a Go monorepo
comfortably. Eight children does not.

A coordinator run that spawns children **costs nothing at all** — it declares `workspace: none`, so
it is rows in SQLite for the twelve hours its children work.

Children get workspaces cheaply, from a kairos-owned bare mirror:

```bash
# one mirror per repo, fetched in the background
git clone --mirror git@github.com:acme/backend.git ~/.kairos/mirrors/github.com/acme/backend.git
git -C ~/.kairos/mirrors/github.com/acme/backend.git config gc.auto 0   # see below

# per run and per child: private refs, private index, borrowed objects, no network
git clone --reference ~/.kairos/mirrors/github.com/acme/backend.git \
    ~/.kairos/mirrors/github.com/acme/backend.git ~/.kairos/work/run_01J8-c1/repo
```

**Why `--reference` clones and not `git worktree`.** Worktrees are cheaper still, and wrong: they share
the mirror's ref namespace and config, so two runs collide in `refs/heads/`, an agent's `git rebase`
or `git config` reaches outside its own workspace, and the mirror's `fetch --prune` can delete a ref a
live run is standing on. A `--reference` clone borrows objects through `objects/info/alternates` and
owns everything else. The one sharp edge: `git gc` in the mirror can repack away objects a borrower
depends on, so mirrors are created with `gc.auto=0` and maintained only when the event log says no
live run references them.

Integration is `git fetch <child-workspace> && git merge FETCH_HEAD` — objects are shared, so there is
nothing to transfer.

---

## A workflow, complete, in 22 lines

```yaml
name: fix-issue
params: { issue: int! }

nodes:
  - id: plan
    actor: claude
    prompt: |
      Read issue #{{ .params.issue }} with `gh issue view {{ .params.issue }}`.
      Break it into tasks. Every task needs at least one acceptance criterion.
    output:
      objective: string!
      tasks: [{ id: string!, title: string!, criteria: [string]! }]

  - id: implement
    actor: claude
    workspace: write
    inputs: { tasks: "$.outputs.plan.tasks", findings: { path: "$.findings", default: [] } }
    output: { branch: string!, sha: string!, summary: string! }
    gates: [build, lint, no-todos, no-secrets, guardrails-untouched]

  - id: approve
    actor: human
    inputs: { summary: "$.outputs.implement.summary", diff: "$.artifacts.diff" }

  - id: pr
    actor: builtin.gh-pr
    inputs: { head: "$.outputs.implement.branch", title: "$.outputs.plan.objective" }
```

What the engine filled in that you did not write: the worktree and its branch, the edges
(`plan→implement→approve→pr`, plus `rejected → back to implement` with the findings attached), the
input schemas, the Go-specific gate commands, the budget, retry-with-a-fresh-worktree on
`implement`, `gh` credentials, and a `y/N` confirmation on `pr` because opening a PR is a
confirm-tier effect.

---

## The guarantees it makes

- **A run survives `Ctrl-C`, a crash, and a reboot.** Its state is the event log, not memory. On
  restart the engine reconciles from the log alone: re-attaches to child processes that outlived it
  (children are started with `setsid`), retries the ones that died, re-arms every timer, and kills
  orphaned process groups it recorded but no longer owns.
- **An agent cannot skip a gate.** The gate schedule is engine data read before the run started.
  Gates run after the agent's process has exited, as children of the engine, with tool paths
  resolved absolutely at preflight. The agent has no code path that reaches them.
- **An agent cannot push, merge, or open a PR itself.** Its environment contains no `GH_TOKEN`, and
  its worktree's push URL is set to a dead scheme. Outward mutations are performed by the engine in
  a separate process after a policy check. Capability, not permission.
- **An LLM that returns malformed output fails its node.** It does not corrupt the next one. Output
  is JSON-Schema validated; the agent gets one in-session repair turn with the validation errors,
  and `kairos check-output` is on its PATH so it can self-check before finishing.
- **You are told what a run cost before it starts and while it runs.**

## The guarantees it does not make

- **No isolation.** An agent can read `~/.ssh`, your other repos, and your browser cookies. The
  optional sandbox (`exec.sandbox: auto`) confines the filesystem on macOS and Linux and is on by
  default, but it is not a kernel boundary and it does not stop exfiltration through a commit.
- **No progress while the binary is down.** State survives; nothing advances. A timer that expired
  while you slept fires on next start. Install the user-level service if you want it always up.
- **No reproducibility of the toolchain.** The host's `go`, `node`, and CLI versions are recorded
  per run, but they are not pinned. Replaying a three-month-old run replays the deterministic half.
- **Concurrency is a correctness hazard, not just a capacity question.** One Docker daemon, one npm
  cache, one `:3000`. Mitigated with per-run `TMPDIR`/`GOMODCACHE`, a port slot pool, and a
  `hostExclusive: true` node flag — not eliminated.

---

## Where to read next

| Doc | Contents |
| --- | --- |
| [`01-architecture.md`](01-architecture.md) | What runs inside the one binary; the eleven laws; the executor chokepoint |
| [`02-config.md`](02-config.md) | Every configuration field, its default, and admission control |
| [`03-workflows.md`](03-workflows.md) | The reduced YAML spec — nodes, edges, gates, waits, spawn |
| [`04-agents.md`](04-agents.md) | Launching `claude`/`codex`/`gemini` headless; typed output; sessions; credentials |
| [`05-gates.md`](05-gates.md) | The constitution mechanism — seven gate kinds, and why they cannot be bluffed |
| [`06-durability.md`](06-durability.md) | SQLite event store, recovery, workspaces, fork/replay, host preflight |
| [`07-surfaces.md`](07-surfaces.md) | The command surface, the TUI, the approval screen, the first sixty seconds |
| [`08-tasksources.md`](08-tasksources.md) | Triggers, polling, dedup, and the stdio plugin contract |
| [`09-limitations.md`](09-limitations.md) | What this genuinely cannot do, stated as consequences |
| [`10-build-plan.md`](10-build-plan.md) | Four phases, the demo that proves each, honest effort estimates |
| [`AGENTS.md`](AGENTS.md) | **The constitution.** Read it before writing code — toolchain, layout, the hard rules, definition of done |

**The laws this variant obeys**, reduced from the original fourteen to eleven, live in
[`01-architecture.md`](01-architecture.md#the-laws). Two are new and worth stating here because they
are the ones the reduction turns on:

- **Execution has exactly one chokepoint.** Every subprocess is created by one package, with an
  explicit cwd inside the run's workspace, an allow-listed environment, its own process group, and
  a recorded event. `os/exec` appears nowhere else in the tree.
- **One binary, one directory, zero setup.** `kairos` on a clean machine with no config file, no
  daemon, and no external service does useful work. Anything that requires setup before first use
  is a bug.
