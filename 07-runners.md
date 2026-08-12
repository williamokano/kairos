# 07 — Runners

Execution happens in exactly one package. That package has more than one implementation: `local`
(always present, the default) and **remote runners** — another machine you own, receiving argv and
returning exit codes, with no isolation and no orchestration.

This document is the one place in the corpus that walks back a previously stated position, so it starts
by saying exactly how far.

---

## 1. The amendment

[`01-architecture.md`](01-architecture.md) (L13′) and [`11-limitations.md`](11-limitations.md) both say
the multi-machine path is **foreclosed, not deferred**. That was written against a specific thing, and
the specific thing is still foreclosed. But the sentence as written is now too broad, and leaving it
would make the corpus lie in the other direction — claiming an impossibility that is in fact a week of
work.

The honest split:

| Foreclosed. Not coming back. | Cheap. This document. |
| --- | --- |
| **Placement** — filter/score/bind over a set of candidates | **Pinning** — a run declares one runner and stays there |
| Scoring functions, least-allocated, spread/pack | Label match, exact, no ranking |
| Workspace **affinity** as a scheduling input | Workspace **locality** as a hard constraint |
| Preemption, eviction, `minPriorityGap` | Nothing. A busy runner queues. |
| Capacity planning, autoscaling, bursting, wake-on-LAN | You edit a config file and add a host |
| Workspace relocation, `allowLossyRelocation` | A run **cannot** move. Ever. |
| Machine registry, join tokens, heartbeat lifecycle, cordon/drain | A health probe and a boolean |
| Capability advertisement with verified controls | `kairos doctor --runner <name>`, which just runs the probe over there |
| **Isolation as a guarantee** | Still none. Now on someone else's disk too. |

The reason the second column is cheap is not luck. It is that the reduction deliberately kept four
properties when it deleted the runtime abstraction: `Spec` stayed pure data, process identity stayed in
the event log rather than in memory, `Chunk` stayed a wire-shaped struct, and `Terminate(ctx, reason)`
stayed the only cancellation API. Those were kept precisely so that a second backend would cost one
package instead of a redesign. This is that package.

### Amendments to apply

1. **`01-architecture.md`, L13′** — retitle from *"One binary, one directory, zero setup"* keeping the
   text, and replace the final clause. Currently: *"This forecloses the twenty-machine path rather than
   deferring it — re-adding it is a rewrite of admission, workspace ownership, and the event bus."*
   Replace with: *"This forecloses the **fleet** — placement, scoring, affinity, preemption, and capacity
   planning are gone and are not coming back. It does not forbid a second machine: a run may be pinned to
   a remote runner ([`07-runners.md`](07-runners.md)), which is one executor implementation and a label
   match, not a scheduler. Zero setup remains the law: `local` is always present and requires no
   configuration."*

2. **`11-limitations.md`, "What is foreclosed rather than deferred"** — keep the section and the closing
   sentence, but narrow the first paragraph to the fleet, and add: *"A modest exception is designed in
   [`07-runners.md`](07-runners.md): additional runners can be plugged in to spread load across machines
   you own. What that does not restore is placement, migration, or isolation — a run is pinned to one
   runner for life and cannot be moved if that runner dies."*

3. **`README.md`**, the "what dies" table — change *"Multi-machine growth path (foreclosed, not
   deferred)"* to *"Placement, affinity, preemption, capacity planning (runners: [`07-runners.md`](07-runners.md))"*.

4. **`AGENTS.md` §7** — the clause forbidding "a remote executor … behind an interface, just in case"
   needs one sentence: *"A remote **runner** is specified in [`07-runners.md`](07-runners.md) and is
   permitted work in phase 4. What remains forbidden is a scheduler: placement, scoring, migration, or
   any per-runner divergence in the event model."*

5. **`05-gates.md`, "The invariant"** — clause 2 ("By the engine") needs the remote qualification in §3
   below. This is the only amendment that weakens a guarantee rather than a scope statement, and it must
   be stated in the gates document itself rather than only here.

---

## 2. The interface

Consumer-defined, in the package that uses it, satisfied by three implementations.

```go
// package engine — defined by the CONSUMER, not by the runners.
type Runner interface {
    Name() string
    Start(ctx context.Context, spec Spec) (Process, error)
    // Probe is the whole of what Describe()/Capabilities used to be: it runs the
    // doctor probe on the far side and returns a Toolchain snapshot. Nothing is
    // self-advertised; everything is executed and observed.
    Probe(ctx context.Context) (doctor.Toolchain, error)
    // Reap kills recorded process groups from the event log alone, with no
    // in-memory state, after a daemon restart. Same contract as local.
    Reap(ctx context.Context, recorded []ProcRecord) ([]ReapResult, error)
}

type Process interface {
    Stdout() <-chan Chunk
    Stderr() <-chan Chunk
    Wait() (Status, error)
    Terminate(ctx context.Context, reason TermReason) error
    Info() ProcInfo
}
```

`Spec` was already wire-safe in every field but one:

| Field | Wire-safe? | Note |
| --- | --- | --- |
| `Tool`, `Args` | yes | logical tool name plus argv. Never a shell string — see §4. |
| `Env` | yes | an explicit allow-list map, never `os.Environ()`. Secrets are not in it. |
| `Stdin` | yes | bytes, or nil. Prompts go here, not in argv. |
| `Timeout`, `Grace`, `StallTimeout`, `Signals` | yes | plain durations and an enum |
| `Limits`, `Nice` | yes | advisory on the far side, same as locally |
| `MaxOutputBytes` | yes | |
| **`Dir`** | **NO** | an absolute host path. **This is the one breaking change.** |

`Dir` becomes a workspace reference plus a relative path, resolved by the runner:

```go
type Spec struct {
    // …unchanged fields…
    Workspace WorkspaceRef // { RunID, Repo }  — the runner resolves this to its own root
    SubDir    string       // relative, cleaned, and REFUSED if it escapes (no "..", no leading "/")
}
```

That one change is worth making even if you never build a remote runner, because it removes host paths
from a struct the engine passes around — which is the same reason `internal/domain` is forbidden from
importing `path/filepath`.

**`Describe()`/`Capabilities` does not come back.** The reduction deleted an eleven-field struct of which
nine were constants, and the two that mattered are answered by executing a probe. `Probe()` returns a
`Toolchain` — the same type `kairos doctor` produces locally — so a remote runner is subject to the same
rule: *never conclude a tool works because it is on `PATH`; execute it.*

---

## 3. The hard part: the workspace

Everything above is easy. This is the part that decides whether remote runners are a week or a quarter.

**Locally the engine reads the tree directly.** That is not incidental — it is load-bearing in five
places:

| What | How it works locally |
| --- | --- |
| `file` gates | `os.Stat` plus doublestar globs, rooted at the workspace |
| `regex` gates | `git diff --unified=0` parsed **in Go**, regex applied to `+` lines only |
| `git-diff` gates | `git` child with fixed argv, assertions evaluated in-process |
| snapshots | `git commit-tree` plumbing against a throwaway index, `update-ref` to a hidden ref |
| artifacts | hash the file in place, then `rename(2)` it into the content-addressed store |

None of that works when the tree is on another machine. So the question is not "can we run a command
remotely" — it obviously can — but "what happens to the guarantees that assumed a local filesystem."

### Gates: which move, and what that costs

| Kind | Local | Remote |
| --- | --- | --- |
| `expr` | in-process over typed JSON | **unchanged — still in the engine.** Needs no workspace. Still unbluffable. |
| `judged` | an actor invocation | **unchanged.** It is a model call over typed output, not a filesystem read. |
| `command` | child, `Dir=<workspace>` | moves to the runner |
| `coverage` | `command` + numeric extraction in Go | command moves; **extraction stays in the engine** — the runner returns stdout, the engine parses the float |
| `file` | `os.Stat` + globs | moves to the runner, which returns a **path list**; glob matching stays in the engine |
| `regex` | Go regex over a parsed diff | the runner returns the **raw unified diff**; parsing and matching stay in the engine |
| `git-diff` | `git` child + in-process assertions | the runner runs `git`; **every assertion stays in the engine** |

The pattern is deliberate and it is the mitigation: **the runner is asked for bytes and exit codes; the
engine keeps the judgement.** A remote `regex` gate does not send a pattern and trust a boolean — it
fetches the diff and matches locally. A remote `coverage` gate does not send a threshold — it fetches
stdout and compares the float. Only `command` genuinely delegates a verdict, because an exit code is all
a command produces.

### The trust consequence, stated plainly

[`05-gates.md`](05-gates.md) clause 2 says gates are evaluated **by the engine**, and that clause is what
makes a gate unskippable. With a remote runner, one word of it becomes conditional:

> **A `command` gate on a remote runner is executed by the runner, which reports its own exit code. The
> engine verifies that the result is well-formed and correlated; it cannot verify that the command
> actually ran.** A compromised or lying runner can report `exit 0` for a lint it never invoked.

What mitigates it:

- **The runner is the `kairos` binary in runner mode**, not a shell script — so gate argv comes from the
  engine's preflight-resolved absolute paths, and the runner has no gate schedule of its own to consult
  or skip.
- **The agent never speaks the runner protocol.** The agent gets a workspace and a prompt; the runner
  protocol is a separate connection with a separate credential, on a port the agent has no token for.
  So the *agent* still cannot skip a gate, which is the threat the gate mechanism exists for.
- **Results arrive as typed, correlated events** carrying argv, exit code, duration, and an output
  digest, streamed on the same channel as the logs. A gate result with no corresponding
  `process.spawned` on the runner is rejected.
- **`expr`, `regex`, `file`, `git-diff`, and `coverage` extraction stay in the engine**, so the majority
  of the constitution — including `guardrails-untouched` and `scope-respected`, the two that matter most
  — is unaffected by a lying runner.

What does **not** mitigate it: nothing else. There is no attestation, no reproducible-execution proof,
and none is planned. **The runner is trusted.** That is a new assumption in the threat model and it is
registered as NL-14 below. It is an acceptable assumption for a machine you own and unacceptable for
one you rent from a stranger, and the docs should say so rather than implying a boundary exists.

### Artifacts, logs, and diffs over the wire

- **Logs** stream as `Chunk` over the same connection with the *same* backpressure ladder, except the far
  side writes to its own file **first** and the engine tails it. That ordering matters: a network hiccup
  must not lose the transcript, and the file is the buffer.
- **Artifacts** are hashed on the runner and pulled by digest, so a re-run producing an identical artifact
  transfers nothing. The `rename(2)`/reflink fast path is gone: a 2 GB artifact is a 2 GB transfer, once.
  `artifacts.maxRemoteBytes` (default 256 MiB) keeps larger ones **on the runner** behind a recorded
  reference, fetched on demand.
- **Snapshots stay entirely on the runner**, as git refs in its own clone. Fork works, and a fork inherits
  the pin. If the runner is gone the fork is refused — exactly as a missing local snapshot is refused
  rather than silently drifted.

### Therefore: a run is pinned for life

```text
run.created{runner: "beelink"}          ← decided ONCE, at admission
   │
   ├── every node execution      → beelink
   ├── every gate command        → beelink
   ├── the workspace + snapshots → beelink, and only beelink
   ├── every child run           → beelink (inheritWorkspace: clone)
   └── artifacts                 → hashed there, pulled here by digest
```

Workspace locality returns in its cheapest possible form. The distributed design made locality a
*scheduling input* — the scheduler scored candidates by where the workspace already was, which is what
dragged in affinity modes, relocation, and their metrics. Here locality is a **hard constraint declared at
admission and never revisited**. There is nothing to score: the run named a label, one healthy runner
matched, and that is the answer for the rest of its life.

A child run may not go elsewhere even when another runner is idle. That is a real loss of parallelism and
the correct trade: the alternative is workspace transfer, which is the first step back into the entire
deleted subsystem.

---

## 4. Runner kinds

```yaml
runners:
  local:                          # always present, cannot be removed, needs no config
    kind: local

  beelink:
    kind: ssh
    host: beelink.lan
    user: william
    identity: ~/.ssh/id_ed25519_pvt
    root: /home/william/.kairos   # the runner's own state dir, mirrors ~/.kairos
    labels: { os: linux, arch: amd64, docker: "true" }
    concurrency: { nodes: 6, pools: { cpu.heavy: 3 } }

  macmini:
    kind: serve                   # `kairos runner serve` on the far side
    endpoint: https://macmini.lan:7718
    token: env:KAIROS_RUNNER_TOKEN
    labels: { os: darwin, arch: arm64, xcode: "16.2" }
    concurrency: { nodes: 4, pools: { cpu.heavy: 2 } }
```

### `local`

Unchanged. Default for every node. Present even when the config file is absent, which is what keeps
L13′ ("zero setup") true.

### `ssh`

For a machine you already have keys to. No agent to install — but the `kairos` binary must be on the far
side, because the runner protocol is spoken by `kairos runner stdio` reading from ssh's stdin.

```bash
ssh -o ControlMaster=auto \
    -o ControlPath=~/.kairos/ssh/%C \
    -o ControlPersist=300 \
    -o BatchMode=yes \
    -i ~/.ssh/id_ed25519_pvt -o IdentitiesOnly=yes \
    william@beelink.lan -- ~/.local/bin/kairos runner stdio
```

Four mechanics that matter:

**Connection multiplexing is not an optimisation.** Without `ControlMaster`, every node execution and
every gate pays a full TCP + SSH handshake — 150–400 ms each, six times over for a run with six gates.
With a persistent master, subsequent channels are ~2 ms.

**Never build a shell command string.** The temptation is
`ssh host "cd $dir && golangci-lint run --new-from-rev=$base"`, and it is wrong three ways: it
re-introduces shell quoting to every argv (a branch name containing a quote becomes an injection), it
turns a `SIGKILL` into exit `137` and loses `Signaled()` — exactly the information that distinguishes an
OOM from a failing test — and it makes the remote path diverge from the local one, so gates behave
differently depending on where they ran. Instead: one long-lived `kairos runner stdio` per connection,
receiving `Spec` as NDJSON on stdin, returning framed `Chunk`/`Status` on stdout. **argv stays argv on
both sides.**

**Process groups and the cancellation ladder are unchanged**, because the far side is the same binary
running the same executor with the same `Setpgid: true`. `Terminate(ctx, reason)` is a *message*; the
ladder — close stdin, TERM the group, grace, KILL, drain the logs — runs local to the processes it kills,
which is the only place it can work.

**Reaping when the network drops** uses the answer already in the corpus. The runner records
`process.spawning{cookie, instance, …}` on its **own** disk before forking. On reconnect the engine asks
it to reap; it compares its own boot id, applies the same ≥2-of-3 identity rule, and kills only what it
verifies. A partition leaves orphans that the far side cleans up itself — the engine never signals a pid
it cannot verify, and never across a network.

### `serve`

The same binary, `kairos runner serve --listen :7718 --token-file …`, speaking **the daemon's own
HTTP/JSON + SSE transport** over TCP rather than a unix socket — chosen over inventing a second protocol
because the framing, resumable `Last-Event-ID` streaming, and client are already written and tested.

Three mandatory differences from the daemon's own listener: a bearer token from a file (not a per-start
random, since the far side must be configurable); TLS required for any non-loopback bind, with the same
refusal-unless-acknowledged rule the web UI uses; and a **strictly smaller route table** — `spec/start`,
`process/signal`, `probe`, `reap`, `artifact/get`, `workspace/*`, and nothing else. A runner cannot start
runs, answer human tasks, or publish definitions. `TestArchitecture_runnerRouteSubset` asserts it, for the
same reason the agent-helper socket has its own subset test.

---

## 5. What is per-runner, and what stays global

This is the distinction most likely to be got wrong, and getting it wrong produces a rate-limit storm.

| Global — one pool, no matter how many runners | Per-runner |
| --- | --- |
| **Model slots** (`strong: 2`) | `nodes` concurrency |
| **Provider rate limits and 5h/weekly windows** | `cpu.heavy` |
| **`dailyUSD` and every budget** | disk, `refuseAt`, `minFreeAbsolute` |
| **The human queue and `maxOpenDecisions`** | the toolchain snapshot |
| `maxQueued` | health |

Your Claude subscription is **one subscription**. Three runners each believing they hold two Opus slots
means six concurrent sessions against a limit of two — exactly the failure the resource model exists to
prevent: retry storms, half-finished work, a confusing bill. So model claims are acquired from the
engine's single admission controller *before* a `Spec` is sent anywhere, and a runner never learns what a
model slot is.

Symmetrically `cpu.heavy` must be per-runner: a Beelink compiling and a Mac mini compiling are not
competing for the same cores, and a global CPU pool would idle both.

```yaml
admission:
  nodes: 4                        # LOCAL runner's concurrency
  pools: { cpu.heavy: 2 }         # LOCAL runner's heavy pool
models:                           # GLOBAL, unchanged
  strong:   { cli: claude, args: [--model, opus],   slots: 2 }
limits:                           # GLOBAL, unchanged
  dailyUSD: 25
runners:
  beelink:
    concurrency: { nodes: 6, pools: { cpu.heavy: 3 } }   # ITS OWN
```

`kairos status` gains one line per runner and keeps one global block:

```text
runners     local     ● 2/4 nodes · 1/2 cpu.heavy · 412 GiB free · go1.25 gh2.63
            beelink   ● 5/6 nodes · 3/3 cpu.heavy ·  88 GiB free · go1.25 docker
            macmini   ◌ unreachable 4m (last seen 14:02) · 1 run pinned
global      strong 2/2 · balanced 1/4 · $6.10/$25 today · 1 decision waiting
```

---

## 6. Selection

Label match. That is the entire algorithm.

```yaml
# workflow level
runsOn: { os: linux }             # every node of every run of this workflow

# node level — permitted, but see the pin rule below
nodes:
  - id: ios-build
    runsOn: { os: darwin, xcode: "16.2" }
```

Rules:

1. **Default is `local`.** A workflow with no `runsOn` never touches a runner and behaves exactly as
   today.
2. **The run is pinned at admission** to the first healthy runner matching the workflow's `runsOn`, in
   config order. Config order, not scoring — deterministic and boring.
3. **A node-level `runsOn` that differs from the run's pin is a publish error**, unless the node declares
   `workspace: none`. A node needing a different machine needs a different *workspace*, and that means a
   child run, not a hop. This single rule is what stops migration, workspace transfer, and the whole
   deleted subsystem from creeping back in through a node field.
4. **No matching healthy runner → the run fails at start**, before spending anything, with the label
   named — reusing the `requires:`/preflight path from [`06-durability.md`](06-durability.md) rather than
   inventing a second failure mode. `run.rejected{reason: no-runner, runsOn: {os: darwin}}`.
5. **`kairos doctor --runner macmini`** runs the probe over there and prints the same table as locally.
   A workflow declaring `requires: [xcodebuild]` is checked against the **pinned runner's** toolchain, not
   the daemon's.

---

## 7. Failure modes

| | What happens |
| --- | --- |
| **Runner unreachable mid-node** | The node's process may still be running over there. The engine does **not** fail the node immediately — it marks the run `Waiting{reason: runner-unreachable}`, releases model slots and permits, and retries the connection with backoff. On reconnect it reconciles exactly as after a daemon restart: alive and verified → **adopt** and resume tailing; dead → `execution.lost` → retry per `restartPolicy`. A 40-minute agent session survives a Wi-Fi blip. |
| **Runner rebooted** | Its boot id changed, so every recorded pgid is meaningless *and* every child is already dead. Reap becomes a no-op, in-flight node executions become `execution.lost`, and — because the workspace clone survived the reboot on its disk — the run continues from the last node boundary. |
| **Runner disk full** | The runner refuses new workspaces at its own `refuseAt` and reports it. Admission stops pinning new runs there; runs already pinned fail their next write node with `workspace.disk.exhausted{runner}`. No migration. |
| **Clock skew** | Durations and `occurred_at` come from the runner; ordering comes from the engine's `global_seq`. So skew corrupts displayed timings, not causality. `doctor` reports skew > 2s as a finding, because a 40-minute node that reports as 3 hours reads as a bug in Kairos. |
| **Runner permanently gone, runs pinned to it** | **The run cannot be migrated. Its workspace is over there.** The engine surfaces it as `Blocked{reason: runner-gone}` and offers exactly three operator choices, none of them automatic: `kairos cancel --compensate` (unwind applied effects and stop), `kairos fork <run> --runsOn local` (re-run from the last node boundary on a new runner, with a **loud** `fork.workspace.unavailable` event, because the tree is not the same tree), or bring the runner back. Pretending a run can be relocated is how you get a fork that silently ran against different files. |
| **Log-stream backpressure over the network** | Same ladder, one extra rung. The runner writes to its own file first, so a slow link degrades the *engine's view*, never the run. If the engine's consumer falls behind: tail ring, `log.degraded{runner}`, and the complete log is still fetchable from the runner's file afterwards. |
| **Credentials on the far side** | The remote machine needs its own `gh auth`, its own agent-CLI login, and its own keychain. Kairos does **not** forward credentials — no ssh-agent forwarding, no token push. The per-run `HOME`, the empty credential helper, and the dead push URL are recreated *by the runner* on its own disk, so the engine still performs every push. `doctor --runner` checks the far side's auth and reports it unauthenticated rather than discovering it 40 minutes in. |
| **Version skew** | The runner reports its `kairos` version on connect. A mismatch in the protocol major version is refused with both versions named. Same binary, same build, or it does not talk. |

---

## 8. Honest limits

New entries for [`11-limitations.md`](11-limitations.md), in its format.

**NL-14 · A remote runner is trusted to report its own gate results.**
A `command` gate executed on a runner returns an exit code the engine cannot independently verify. A
compromised, misconfigured, or simply lying runner can report `exit 0` for a lint it never ran, and the
gate mechanism — the thing carrying the entire safety budget in a variant with no isolation — is only as
honest as the runner.
*Blast radius:* every `command` and `coverage` gate on that runner, therefore every quality claim about
runs pinned to it.
*Mitigations:* the runner is the `kairos` binary in runner mode with no gate schedule of its own
(**shipped**); gate argv is engine-chosen from preflight-resolved absolute paths (**shipped**); `expr`,
`file`, `regex`, `git-diff`, and `coverage` extraction stay in the engine, so `guardrails-untouched` and
`scope-respected` are unaffected (**shipped**); results must correlate with a `process.spawned` on the
runner (**shipped**); the agent has no runner credential and so still cannot skip a gate (**shipped**);
execution attestation (**none, and not planned**).
*Detection:* **none.** A lying runner looks exactly like a passing gate. Use runners you own.

**NL-15 · The agent inherits the *runner's* credentials and filesystem.**
NL-01 said an agent can read anything you can. On a remote runner it can read anything that machine's
user can — a different, possibly larger, set: that host's `~/.ssh`, its cloud credentials, its other
repos, anything it can mount. Adding a runner adds a blast radius rather than moving one.
*Blast radius:* the runner's entire user account, plus whatever that account can reach on the network it
sits on.
*Mitigations:* per-run `HOME` and env allow-list recreated on the runner (**shipped**); the engine holds
push credentials, so the runner needs none for effects (**shipped**); a dedicated OS user on the runner
(**planned**, documented not defaulted); the same opt-in sandbox flags apply per-runner (**shipped**).
*Detection:* none, as NL-01.

**NL-16 · A run pinned to a lost runner cannot be recovered, only abandoned or re-derived.**
Because the workspace is on that machine and there is deliberately no transfer mechanism. Uncommitted
work in that clone is unreachable while the runner is down and lost if it never returns.
*Blast radius:* one run per lost runner, plus its children.
*Mitigations:* `Blocked{reason: runner-gone}` with three explicit operator choices rather than a silent
retry loop (**shipped**); `fork --runsOn` emitting a loud `fork.workspace.unavailable` (**shipped**);
snapshots pushed to the project's git remote at node boundaries when configured (**planned** — this is
the one mitigation that would actually recover the work).
*Detection:* loud. The run sits `Blocked` with the runner named.

**NL-17 · Remote artifacts and diffs cost a network transfer.**
The local `rename(2)`/reflink fast path is gone. Large artifacts either transfer or stay remote behind a
reference, and a `git-diff` gate on a 20 MB diff moves 20 MB before it can be evaluated.
*Blast radius:* wall-clock and bandwidth, not correctness.
*Mitigations:* content-addressed pull, so identical artifacts transfer once (**shipped**);
`maxRemoteBytes` with fetch-on-demand above it (**shipped**); diffs fetched once per gate batch rather
than per gate (**shipped**).
*Detection:* transfer duration is in the run timeline.

---

## 9. Effort, and where it lands

**~18 developer-days (14–28).** Not phase 0, not phase 1, and not phase 2 — every one of those is
prerequisite. It belongs as a **phase 4**, after the web UI, because it depends on: the executor's
recorded-process-identity contract (phase 0), the reconciliation loop it plugs into (phase 0), effects
holding credentials on the engine side (phase 1), and snapshots at node boundaries (phase 2).

| | days |
| --- | --- |
| `Spec.Dir` → `Workspace` + `SubDir`, and the gate split (engine keeps judgement) | 4 |
| `kairos runner stdio` + `serve` modes, route-subset test, version handshake | 4 |
| the `ssh` runner: multiplexing, framing, remote reaping | 3 |
| per-runner admission, global model pools, `kairos status` | 2 |
| `runsOn` matching, the pin, publish validation, `doctor --runner` | 2 |
| failure-mode tests: unreachable mid-node, reboot, runner-gone | 3 |

**The demo that proves it:**

> `runsOn: {os: linux}`, daemon on a Mac, runner on a Linux box. A run executes an agent, four gates, and
> a PR — the PR opened **by the engine**, with no GitHub credential ever present on the runner. Then pull
> the runner's network cable mid-agent: the run goes `Waiting{runner-unreachable}` and **releases its model
> slot** so other work proceeds. Plug it back in and the run **adopts** the still-running agent, finishing
> without re-spending a token. Reboot the runner mid-run instead and watch it resume from the last node
> boundary. Finally `kairos status` shows two runners with independent `cpu.heavy` and **one** shared
> `strong` pool at 2/2.

The cable step is the one worth building the feature for. If unplugging a cable costs forty minutes of paid
agent work, a runner is a liability rather than capacity.

---

## What this deliberately does not become

- **A scheduler.** No placement, no scoring, no bin-packing, no rebalancing.
- **An autoscaler.** No provisioning, no cloud API, no wake-on-LAN, no bursting.
- **A migration system.** A run never moves. There is no workspace transfer, and adding one re-opens the
  entire deleted subsystem.
- **A cluster.** No membership protocol, no gossip, no leader election, no quorum. The engine is the only
  brain and runners are dumb.
- **An isolation boundary.** A runner is a *different* machine, not a *safer* one.
- **Multi-tenant.** Still one operator, and now they own two machines.
- **A reason for the engine to be highly available.** The daemon remains a single process on one host. If
  it is down, nothing progresses — on any runner.
