# 01 — Architecture

One OS process, one directory, one SQLite file, and one place where child processes are born.

---

## What runs inside the binary

```text
┌──────────── kairos daemon — one process, root ~/.kairos ───────────────────────┐
│ supervisor: flock ─ config ─ migrate ─ RECOVER ─ errgroup(ctx) ─ signals       │
│                                                                                │
│  ┌── adapters (no business logic) ────────────────────────────────┐            │
│  │ http api on ~/.kairos/daemon.sock  ·  SSE fan-out              │◄─ kairos   │
│  │ tasksource pollers  ·  inbox watcher  ·  notifier              │◄─ TUI      │
│  └──────────────────────────┬─────────────────────────────────────┘◄─ browser  │
│                             │ commands in / events out                         │
│  ┌──────────────────────────▼── engine ───────────────────────────┐            │
│  │ runShards[N]: event → hash(runID) → one goroutine, in order    │            │
│  │   load state ─► domain.Advance(state, ev, now)  PURE ─► []Cmd  │            │
│  │   append RunAdvanced{cmds}  ── decision BEFORE action ──►      │            │
│  └────────┬───────────────────────────────────┬──────────────────┘            │
│           │ commands                          │ outcome events                 │
│  ┌────────▼──── services ────────────────────┴──────────────────────┐         │
│  │ admission · workspaces · executor · gates · effects · humanqueue  │         │
│  │ artifacts · timers · reaper · registry                            │         │
│  └───┬──────────────────────┬──────────────────────┬────────────────┘         │
│      │ AppendIf             │ spawn                │ probe                     │
│  ┌───▼── eventstore ────────┴───┐          ┌───────▼── RealityProbe ───┐       │
│  │ ONE WRITER GOROUTINE          │          │ pgid + nonce liveness     │       │
│  │  BEGIN IMMEDIATE              │          │ output.json present?      │       │
│  │   validate → redact → INSERT  │          │ workspace dirs on disk    │       │
│  │   apply projections + offset  │          │ effect probe by idem key  │       │
│  │  COMMIT (fsync) → bus.Publish │          └───────────────────────────┘       │
│  │ readers: N conns, WAL, never blocked                                        │
│  └───────────────┬───────────────┘                                             │
│                  └──► in-process bus → engine · SSE · TUI · notifier           │
└────────────────────────────────────────────────────────────────────────────────┘
        │ os/exec: Setpgid, cwd=~/.kairos/work/<run>/repo,
        │          HOME=~/.kairos/work/<run>/home, env from an allow-list
        ▼
   ┌── one process group per node execution ─────────────────────────────────┐
   │ claude / codex / go test / golangci-lint      (host toolchain, host PATH)│
   │ <workspace>/.kairos/: input.json output.json progress.jsonl              │
   │ <run>/<nodeExec>/: stdout.log stderr.log proc.json{pgid,nonce,startedAt} │
   └─────────────────────────────────────────────────────────────────────────┘
        │ network: UNRESTRICTED by default. No egress allowlist exists.
```

Goroutines at rest: ~14. With four concurrent runs: ~35.

## The daemon/client split, and why it is not optional

`kairos` with no arguments does two things: ensures a daemon is running, then attaches a TUI to it.
They are the same binary in two roles, separated by a unix socket.

This looks like avoidable ceremony for a local tool. It is the single most important structural
decision in the reduction, for one reason: **if the TUI owns the engine, closing your terminal kills a
three-hour run.** "Durable workflows" and "Ctrl-C loses my work" cannot share a process lifetime. The
socket also means SQLite has exactly one writer, that the TUI and the web console cannot diverge
(neither holds state), and that `kairos do "…"` in a script works with no TTY.

```text
kairos                 ensure daemon (flock) + attach TUI.  Ctrl-C detaches; work continues.
kairos serve           daemon only, foreground — for launchd / systemd --user
kairos <verb>          a client over the socket; starts a daemon if none is running
kairos down            stop the daemon: drain, or terminate process groups and record it
```

Auth is the filesystem: `~/.kairos` at `0700`, `daemon.sock` at `0600`, and a peer-credential check
(`SO_PEERCRED` / `LOCAL_PEERCRED`) requiring the connecting uid to match. No tokens, no TLS, no OIDC —
an entire surface and its bug class deleted.

One sharp edge that must not be missed: **the agent you spawn runs as your uid and can reach that
socket.** So the helper endpoint agents are given (`kairos check-output`, `artifact stage`,
`ask-human`) is a *separate, narrow* socket with its own route table, and a test asserts it is a
strict subset that excludes `answer-task`, `publish`, and `admin`. An agent that can approve its own
gate has defeated the entire safety model.

---

## The laws

Fourteen in the ancestor corpus. Eleven survive, three change, and one is added. The ones that change are
where the reduction actually happens, so they're stated in full; the added one (**L15**) is what keeps this
a machine you delegate to rather than a bot with an agenda.

| | |
| --- | --- |
| **L1** | **Workflows are durable.** A run survives Ctrl-C, a crash, a reboot, and a week-long wait. Its authoritative state is the embedded event log. *Forbids:* run state in memory, goroutines-as-workflows, `time.Sleep` as a wait primitive. **Timer waits are absolute wall-clock deadlines in the store, never sleeps** — the laptop will suspend. |
| **L2** | **Events are immutable; state is a projection.** Unchanged. Still the top tie-breaker. |
| **L3** | **Actors are replaceable.** Unchanged. Claude → Codex → a shell script → a human changes no graph. |
| **L4′** | **Execution has exactly one chokepoint.** *(replaces "runtimes are pluggable")* Every subprocess is created by one package, with an explicit cwd inside the run's workspace, an allow-listed environment, its own process group, and a recorded event. *Forbids:* `os/exec` anywhere else in the tree, an actor spawning processes the executor did not create, inherited environments. *Because:* it preserves the half of L4 that mattered — blast radius is one package — and it is where sandboxing, auditing, and reaping get implemented once. |
| **L5** | **Workspaces belong to runs.** Unchanged, and it matters *more*: with no isolation, single-writer discipline is the only thing stopping two runs corrupting each other's trees. |
| **L6′** | **Execution is confined and recorded.** *(replaces "only Workers execute")* The engine, domain, store, and projection packages never exec. All execution goes through the executor, always with cwd inside the owning run's workspace. Say the loss in the same breath: **the trusted computing base is now the entire user account.** L6′ does not buy what L6 bought. |
| **L7** | **Admission is not domain-aware.** It knows pools, capacities, priorities, and queue policy. It has never heard of Go. Placement, labels, and affinity are deleted as vacuous. |
| **L8** | **Contracts are typed and validated.** Unchanged, and now the **most valuable law in the system** — with isolation gone, typed contracts plus gates carry the whole safety budget. |
| **L9** | **Nothing is a special case.** Human approval is a node. CI waiting is a node. A chat message is a run with one node. *Biggest implementation risk in this variant*: "a binary I open that just does the work" is exactly the pressure that produces an ad-hoc fast path. A prompt typed in the TUI must become a `trigger.received` event and a one-node run through the same handler the HTTP API uses. |
| **L10** | **Failure is explicit, never silent.** Unchanged. Every fallback emits an event naming what failed and what was substituted. |
| **L11** | **Every effect has a compensation.** Unchanged, with a companion sentence: it governs effects *kairos* performs. An agent that runs `gh pr merge` itself has performed an undeclared, unrecorded, uncompensable effect. That gap is new and registered. |
| **L12** | **Determinism where possible, quarantine where not.** Unchanged. Replay only works if the deterministic part actually replays. |
| **L13′** | **One binary, one directory, zero setup.** *(replaces "useful on one machine, unchanged on twenty")* `kairos` on a clean machine with no config file, no daemon to install, and no external service does useful work. *Forbids:* a required config file before first run, a mandatory external process, an install step beyond the binary. **This forecloses the *fleet*** — placement, scoring, affinity, preemption, and capacity planning are gone and are not coming back. It does **not** forbid a second machine: a run may be pinned to a remote runner ([`07-runners.md`](07-runners.md)), which is one executor implementation and a label match, not a scheduler. Zero setup remains the law: `local` is always present and requires no configuration. |
| **L14** | **The corpus is the source of truth.** Unchanged. |
| **L15** | **Kairos never invents work.** *(new)* Every Run traces to a trigger the user configured or a task the user filed, and that trigger is named in the Run's first event. *Forbids:* an idle loop that looks for something useful to do; an actor spawning a Run because it noticed something outside its task; a "suggestions" feature that acts rather than proposes; a scheduled job the user did not configure; any Run whose `trigger.received` cannot be traced to a configured source, a schedule, a filed task, or a parent Run. *Because:* this is the line between a machine you delegate to and an assistant with an agenda, and it is the property that makes everything else auditable — if work can appear spontaneously, "why did it do that" stops having an answer. A classifier proposing a draft for approval is not self-direction; a classifier deciding to send it is. |

**Tie-breakers, reordered.** Originally: event log → isolation/safety → durability → simplicity → DX.
Now: **1.** correctness of the event log · **2.** durability · **3.** containment (workspace
confinement, gates, human approval on irreversible effects) · **4.** simplicity of the core · **5.** DX.

Isolation drops from second place because there is none. Its replacement is genuinely weaker and must
not be ranked as though it were equivalent.

---

## The executor: the one chokepoint

```go
// The whole of what used to be five runtime providers behind a five-method interface.
type Executor interface {
    Start(ctx context.Context, spec ExecSpec) (Started, error)
    Signal(ctx context.Context, pgid int, sig syscall.Signal) error
}

type Started struct {
    PGID, PID int
    Nonce     string     // passed to the child in its env; defeats PID reuse
    StartedAt time.Time
    Dir       string
}
```

Four spawn details that are load-bearing rather than incidental:

**`SysProcAttr{Setpgid: true}`.** Children are not in the daemon's process group, so `Ctrl-C` in your
terminal does not reach them — the daemon decides their fate and *records the decision first*. It also
detaches them from the controlling terminal, so a stray `read` in an agent's shell command cannot
steal keystrokes from the TUI. That is a real bug you hit on day one otherwise.

**stdout and stderr go to files, never to pipes the daemon owns.** A pipe dies with its reader, so a
`SIGKILL`ed daemon turns a working agent into a process that dies messily on its next write with its
output lost. Files mean the killed attempt's last 200 lines — usually the reason it wedged — are on
disk regardless, and they make process *adoption* possible (below).

**`process.spawning` is committed before `fork/exec`.** Same reasoning as recording an effect before
calling it: a crash in the gap leaves a discoverable fact and a nonce recovery can hunt for in the
process table, instead of a process nothing knows about.

**Identity is `(bootID, pgid, startedAt)`, never a bare pid.** PIDs are reused. Killing pgid 41233 at
startup because a crashed run once used it means killing a stranger's process tree — quite possibly
your editor. A boot-id mismatch means "everything in flight is unverifiable; signal nothing."

Cancellation, in order, no steps skipped: close stdin (many CLIs exit cleanly on EOF alone) →
`kill(-pgid, SIGTERM)` → wait `killGrace` → `kill(-pgid, SIGKILL)` → drain the log files for two more
seconds → record `process.reaped`.

The known gap: a **double-forked daemon** — a dev server, a file watcher, `docker run -d` — escapes a
process-group kill. Mitigations are a process-inventory diff per node execution, a startup orphan
reaper, and cgroup-v2 kill on Linux where available. It is not closed; see
[`11-limitations.md`](11-limitations.md).

---

## Architecture tests

Four survive from the original, five are new. They are cheap, and in this variant several of them are
the *only* thing holding a boundary that used to be held by a network or a kernel.

```go
TestArchitecture_domainPurity
//   internal/domain imports nothing from internal/ and none of: os, os/exec, net,
//   database/sql, syscall, math/rand, time.Now.
//   NEW forbidden entry: path/filepath. In a host-local system the pull to put a
//   workspace path into the domain is constant, and a domain that knows about paths
//   is a domain that stops replaying identically.

TestArchitecture_noExecOutsideExecutor
//   os/exec, syscall, and x/sys are importable ONLY by internal/executor/local.
//   internal/workspace runs git THROUGH the executor, so every git invocation is a
//   recorded host.command.executed event. One chokepoint, one audit log (L4′).

TestArchitecture_tuiHasNoExecution
//   internal/tui imports neither os/exec nor internal/executor/*, and imports the
//   API client rather than the engine.
//   ⚠ In the distributed design this boundary was ALSO a network boundary. Here this
//   test is the only thing left holding it, and the first person who wants a
//   "just for benchmarking" shortcut will be right that it works. Treat a request to
//   weaken it as a request to delete the boundary.

TestArchitecture_dependencyDirection
TestArchitecture_noSQLOutsideStore            // makes the driver a one-file swap
TestArchitecture_processesRecordedBeforeSpawn // AST: a recorder call precedes every cmd.Start
TestArchitecture_noOsExitOutsideMain          // a stray log.Fatal now kills the engine mid-dispatch
TestArchitecture_agentSocketRouteSubset       // an agent cannot answer its own approval
TestArchitecture_singleWriter                 // only the writer goroutine holds the write conn

TestEngine_everyRunHasATraceableTrigger       // L15: no Run exists without a configured
//   trigger, a schedule, a filed task, or a parent Run naming it. Runs the assertion over a
//   corpus of recorded runs AND over a fuzzed event log, because the failure mode is a Run
//   appearing with an empty or synthesised trigger — which is what "it decided to be helpful"
//   looks like in the data.
```

Each has a `//go:build violation` fixture package, and `make arch` must fail when the fixture is
enabled — an architecture test nobody has seen fail is a test nobody knows works.

---

## Toolchain

| Purpose | Choice | Note |
| --- | --- | --- |
| Language | Go, latest stable | |
| Datastore | **`modernc.org/sqlite`** (pure Go, no cgo) | `CGO_ENABLED=0` cross-builds for darwin/linux × arm64/amd64 from one machine, in one `make release`, with no C toolchain. For a tool whose entire pitch is "one binary," this dominates the ~2× write-speed loss — and the write volume is one user's events, three orders of magnitude below where it matters. Imported from exactly one file, so `mattn/go-sqlite3` behind a build tag stays a one-file change. |
| HTTP | `net/http` | on a unix socket |
| CLI | `spf13/cobra` | |
| TUI | `charmbracelet/bubbletea` + `lipgloss` | |
| JSON Schema | `santhosh-tekuri/jsonschema/v6` | draft 2020-12 |
| Compression | `klauspost/compress/zstd` | log rotation, archives |
| IDs | `oklog/ulid/v2` | |
| Process groups | `golang.org/x/sys/unix` | importable **only** by the executor |

Deleted from the original's dependency table: `pgx`, `golang-migrate`, `grpc`, `protobuf`, `buf`,
`testcontainers`, `bbolt`. Migrations become a ~90-line runner over an `embed.FS`, forward-only, with
`VACUUM INTO` taking a hot consistent backup before every migration. Integration tests use a real
SQLite file in `t.TempDir()` and a **fixture `PATH`** built per test — an integration test that reads
the ambient `PATH` is a flaky test, because the risk moved from "is Postgres real" to "is the
developer's `git` 2.39 or 2.45."
