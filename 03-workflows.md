# 03 — Workflows

A workflow is a graph of typed nodes. Each node names an actor, declares a JSON Schema for its output,
and lists the gates that must pass before the run moves on. That is the whole model.

The reduction to one machine deletes about half the YAML surface — everything that existed to tell a
scheduler where to put work — and adds defaults so that a real workflow fits in twenty lines.

---

## A complete workflow

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

```console
$ cd ~/src/acme-backend && kairos run fix-issue.yaml --issue 421
```

Everything the engine filled in that you did not write:

- **the workspace** — a `--reference` clone at `~/.kairos/work/run_01J8/repo` on branch
  `kairos/run_01J8` off `HEAD`, plus a per-run `HOME`
- **the edges** — `plan → implement → approve → pr → $succeed` from document order; `failure` and
  `timeout` → `$fail`; **`rejected` → back to the same node** with the gate findings attached
- **input schemas** — inferred from the `inputs` keys
- **the gate commands** — `build`/`lint`/`test` specialised from the detected `go.mod`
- **the budget** — from config, printed before the first token is spent
- **retry** — 2 attempts with a fresh workspace on `implement`, because it is a write node with an
  agent actor
- **credentials** — `gh` auth from the host keychain, injected into the `pr` node's process only
- **a confirmation** — `y/N` before `pr`, because opening a PR is a confirm-tier effect

---

## What died from the spec

| Field | Why |
| --- | --- |
| `worker` (the whole block) | There is one runtime. A field with one legal value is noise. |
| `runtime: firecracker \| docker \| k8s \| ssh` | Collapsed. Nothing to select. |
| `image:` | The host *is* the image. Tool presence is asserted by preflight, not provisioned. |
| `resources.cpu / memory / disk` | These were *placement inputs* for a scheduler choosing a machine. With one machine there is no choice to inform, and they are not replaced by cgroup limits — those are a config concern, not a workflow concern. |
| `features: {docker, systemd, kvm}` | Host capability: discovered, not requested. |
| `pool`, `ttl`, `holdThreshold` | No warm pools, no VM to expire. `holdThreshold` existed solely to decide whether releasing a VM was worth the reboot cost; both sides of that trade are now zero. |
| `network.egress` / `allow` | **The one real capability loss.** You cannot allowlist egress for a host process without root. Shipping the field anyway would be a field that lies — so it is a hard publish error naming that the local executor enforces no egress control. |
| machine selectors, affinity, taints, tolerations, `placement` | One machine. If Xcode isn't installed, preflight fails with "xcodebuild not on PATH" — honest, and better than `NotSchedulable`. |
| `workspace.size` | No volume to size. |
| `workspace.template.caches` + the shared-CoW-cache mode enum | `~/go/pkg/mod`, `~/.npm`, `~/.cargo` are simply *there*, warm, and already correct. **The single largest simplification in the reduction.** |
| `workspace.suspension.*` (`preserveDirtyOnSuspend`, `dirtyPatchArtifact`, `onCaptureFailure`) | These existed because a released VM destroyed uncommitted work. A directory on your own disk is not reclaimed by anyone. Retained in one place only: before a `freshWorkspace` retry, which destroys work *by design*. |
| `node.memory.graph` | No knowledge graph in this variant. |
| `node.materialize` | Artifacts are already files on the host; use `context:`. |
| `inputSchema` **required** | Downgraded to optional and inferred. See below. |
| `outputSchema` **required** | **Stays required.** Non-negotiable. |

---

## The defaults that let you omit almost everything

| Thing | Default | Why it is safe |
| --- | --- | --- |
| `on` (edges) | `success` → next node in **document order**; last → `$succeed`. `failure`/`timeout`/`denied` → `$fail`. **`rejected` → self**, bounded by `loopGuard`. | The invariant "every outcome routes somewhere" is satisfied *structurally* instead of by the author retyping it. And `rejected: self` gives you the fix-the-findings loop for free — the most common edge in the whole corpus — while keeping the graph acyclic in the author's head. |
| `inputSchema` | `{type: object, required: [<keys of inputs>]}` | A separate, stronger rule already catches the real failure: an unresolved JSONPath selector without a `default` fails the node *before* the actor is invoked. What inference loses is type-checking of input *values*, which for a single-author local tool is a small fraction of the value and a large fraction of the authoring cost. |
| `workspace` | `read`, sourced from the repo containing `cwd` | You are standing in the repo. That's the premise. |
| `actor` | Built-ins compiled in: `claude`, `codex`, `gemini`, `local` (ollama), `shell`, `human`, `noop`, and `builtin.*` (`git-commit`, `git-push`, `gh-pr`, `gh-comment`, `gh-checks`, `jira`) | Zero actor YAML on day one. An actor definition is only needed to *override* a built-in profile. |
| `gates` | A built-in library **specialised by detected toolchain**: `go.mod` → `go build ./...`, `golangci-lint run --new-from-rev={{.base}}`, `go test -coverprofile`; `package.json` → `npm run build`/`eslint`/`vitest`; `Cargo.toml` → `cargo build`/`clippy`/`cargo test`. Detected once into `.kairos/detected.yaml`, editable. | This is the difference between "gates exist" and "gates get used". Nobody writes forty lines of constraint YAML to get a lint gate. |
| `limits` | `wallClock 2h`, `maxCostUSD 10`, `maxNodeExecutions 100`, `maxSpawnDepth 2`, `loopGuard 3` | Preserves the intent of the original's *required* limit fields — no surprise bills — without the ceremony. Printed in the header before the run starts. |
| `retry` | `1` attempt; but `2` with `retryOn: [schema-invalid, failure]` and `freshWorkspace: true` when the node is `workspace: write` with an agent actor | Locally a fresh workspace is a fresh `--reference` clone: ~1s, no snapshot support required. The original's caveat ("off by default because snapshot support is runtime-dependent") dissolves. |
| `timeout` | the actor's default, else `30m` | |

---

## Nodes

```yaml
- id: implement
  actor: claude
  prompt: |                       # Go-templated over .params / .inputs / .project
    Implement the open tasks. …
  promptFile: .kairos/prompts/impl.md      # alternative

  inputs:                         # JSONPath over run memory
    tasks:    "$.outputs.plan.tasks"
    findings: { path: "$.outputs.review.findings", default: [] }

  output:                         # shorthand → strict JSON Schema
    branch: string!               # `!` = required
    sha: string!
    summary: string!
  outputSchema: { $ref: schemas/change-result.json }   # alternative, mutually exclusive

  context: [.kairos/constitution.md, AGENTS.md, docs/architecture.md]

  workspace: write                # none | read | write     (default: read)
  workspacePaths: ["services/**"] # write scope, enforced by a gate (not a mount)
  hostExclusive: false            # serialise against other runs for shared host state

  resources: { model: { class: strong, slots: 1, maxCostUSD: 12 } }
  timeout: 90m
  sessionAffinity: run            # execution | node | run

  retry:
    maxAttempts: 2
    retryOn: [failure, timeout, schema-invalid]
    freshWorkspace: true
    mutate:
      - { attempt: 2, resources: { model: { class: strong } } }
      - { attempt: 3, actor: codex }      # a different CLI, same typed contract — L3 paying off

  gates: [build, lint, no-todos]
  effects: []                     # executed BY THE ENGINE, never by the agent
  artifacts: [{ name: diff, from: "git diff {{ .base }}", kind: patch, always: true }]
  wait: {}                        # human | timer | poll | child-run | conversation
  spawn: {}                       # + join
  optional: false
```

`resources.model.class` names a *class* (`strong` / `balanced` / `cheap` / `local`), never a model
name. Config maps classes to CLIs and slot counts, so swapping models is a config edit and no workflow
changes.

`hostExclusive: true` is new and exists only because isolation is gone: it serialises a node against
every other run for the duration, for nodes that touch global host state — one Docker daemon, one
`:3000`, one global npm cache. See [`09-limitations.md`](09-limitations.md).

---

## How a waiting node holds nothing

This is the question the distributed design spent the most machinery on, and locally it dissolves.

In the original, "a waiting node holds no compute" was an *achievement*: snapshot the workspace,
release the worker lease, capture the dirty tree, mark the workspace reclaim-eligible. Five steps, and
uncommitted-work loss was the residual damage.

**Locally, a waiting node never had anything to hold.** The engine does not fork a process and block
it. It appends an event and returns. There is no teardown because there was no setup. A wait's entire
footprint is three rows:

```sql
events   node.execution.waiting{execID, waitKind, waitSpec, timeoutAt}
waiters  (run_id, exec_id, kind, match_json, next_poll_at, timeout_at)  -- indexed on next_poll_at
timers   (run_id, exec_id, fire_at, on_timeout_action)
```

plus one `time.Timer` in the engine keyed off `MIN(next_poll_at, fire_at)` across *all* waiters. Zero
processes, zero goroutines per wait. `SIGKILL` the daemon and the wait survives, because the wait **is**
the rows.

What entering `Waiting` releases, exactly — three named things and only these:

| Held while executing | Released on `Waiting` |
| --- | --- |
| the child process | already exited, or never spawned |
| an admission permit + a model slot | yes |
| the workspace write lock (`flock`) | yes |

The workspace **directory** is not released. Disk is cheap and nobody is reclaiming it, so the run's
files sit exactly where they were, dirty or clean. That deletes seven mechanisms from the original spec
whose only purpose was managing the consequence of a workspace disappearing.

### `ci-watch`

```yaml
- id: ci-watch
  actor: builtin.gh-checks
  workspace: none
  inputs: { sha: "$.outputs.implement.sha" }
  wait:
    on:
      - kind: poll                          # the local default — a laptop has no public URL
        command: ["gh", "run", "list", "--commit", "{{ .input.sha }}",
                  "--json", "status,conclusion,url"]
        until: "all($.[*].status, . == 'completed')"
        every: 30s
        backoff: { factor: 1.5, max: 5m }
      - kind: webhook                       # opt-in, if you run `gh webhook forward`
        endpoint: "127.0.0.1:7777/hooks/github"
        match: { event: check_suite.completed, expr: "$.payload.head_sha == $.input.sha" }
    timeout: 3h
    onTimeout: escalate
  output: { conclusion: string!, failureAnalysis: string }
```

Watching an 18-minute CI run on a 30s→5m backoff is ~15 subsecond `gh` invocations — about four
seconds of total CPU. Between them the node holds nothing at all.

### `human-approval`

```yaml
- id: approve
  actor: human
  workspace: none
  inputs: { summary: "$.outputs.integration.summary", cost: "$.meta.costSoFar",
            diff: "$.artifacts.diff" }
  output: { decision: string!, reason: string }
  wait:
    on: [{ kind: human }]
    timeout: 72h
    onTimeout: park          # REQUIRED FIELD — see below
```

Append `human.task.created`, insert a row, release everything, return. The TUI's approval screen, the
`kairos approve` verb, and the web console are three renderers of that one row.

**`onTimeout` stays a required field, unpublishable without it**, and this is the one wait rule that
must not relax. A 72-hour approval that silently proceeds is the worst available behaviour, and locally
it is *easier* to get wrong because there is no scheduler forcing the issue.

But the recommended value changes. On a fleet, a parked run leaked a workspace and a queue slot, so
`escalate-abandon` was right. Locally a parked run costs one directory, and the usual cause of a
timeout is "the laptop was shut for four days," not "nobody cares." So **`park` is added as a third
outcome and becomes the default**: it never proceeds and never fails, it just waits and shows a badge.
Auto-approve on timeout is a publish-time error, not a discouraged option — it is the exact mechanism
by which a gate becomes latency plus a false sense of control.

---

## Loops without cycles

Review findings become *work items*, not a retry edge. The default `on.rejected → self` means the
author who writes nothing gets the correct loop: findings land in `$.findings`, feed the same node's
`inputs`, `iteration` increments, and `loopGuard` bounds it. The graph the author reads stays linear;
the iteration count is queryable and capped.

```yaml
limits:
  loopGuard: { maxIterationsPerNode: 4, onExceeded: escalate-to-human }
```

---

## Fan-out on one machine

```yaml
- id: fanout
  spawn:
    workflow: implement-task
    forEach: "$.outputs.breakdown.tasks"
    strategy: bounded(3)
    inheritWorkspace: clone       # was `snapshot`
  join: { mode: waitAll, onChildFailure: degrade }
```

A coordinator run declares `workspace: none`, so it costs **nothing at all** — rows in SQLite for the
twelve hours its children work. Children get `--reference` clones off the same mirror in about a second
each, and integration is `git fetch <child> && git merge FETCH_HEAD` with no network transfer.

What "parallel" means here is precise and worth stating, because it is where naive local designs fall
over: `strategy: bounded(3)` bounds how many *child runs* are live, and independently the `cpu.heavy`
semaphore bounds how many *build/test/lint commands* run at once across the whole binary. Four
children can all be live — agents thinking, clones checked out — while only two are compiling. An
agent CLI is network-bound; its tool calls are not.

`Degraded` survives as a first-class state, resolvable only by a coordinator node. `maxSpawnDepth`
defaults to 2 and matters *more* locally: without it a coordinator eventually spawns a coordinator,
which on your laptop means forty processes and a surprising bill.

**Waves survive, but for a different reason.** On a fleet they served both correctness (don't
parallelise conflicting blast radii) and resource shaping. Admission now does the shaping
automatically, so waves are purely about correctness: two children editing the same package must not
race. The independence check drops from a knowledge-graph blast-radius query to a cheap structural one
— compare the tasks' declared `paths[]` for overlap, in-process, free. That is a real downgrade in
sophistication and an acceptable one: path overlap catches most of it, and `git merge` conflicts catch
the rest loudly.

---

## DSL: keep YAML, defer the DSL

The four hard requirements that made YAML the right first choice all survive — hash-stable,
machine-generatable, diffable, parseable without a language runtime — and the third gets *more*
important, not less: a local binary running autonomously with your own credentials is precisely the
case where the definition must be readable in a diff.

Meanwhile the strongest argument *for* a DSL — copy-pasted review panels across five workflows — is
answered far more cheaply by the things this variant needs anyway: built-in actors, a gate library
specialised by detected toolchain, document-order edges, inferred input schemas, and the `output:`
shorthand. Together they take the reference workflow from ~200 lines to ~22, and cost days rather than
the weeks a parser, type checker, formatter, and LSP would.

The cheaper front end to try first is `kairos new --from "read the issue, implement it, gate it, open a
PR"` generating canonical YAML you then edit. If that lands well, the DSL may never be worth building.
