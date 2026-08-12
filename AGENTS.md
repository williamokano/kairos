# AGENTS.md — constitution for coding agents in this repository

Any agent or human implementing Kairos obeys this file. It outranks convenience, outranks your own
taste, and outranks "it works". If a document contradicts this file, **this file is wrong** and must be
amended by an ADR — do not silently deviate.

## 0. Read before you write

This repository is design, not code. Eleven documents, and you read the closure of what you are
building, not all of it:

| Always | Then, for what you are building |
| --- | --- |
| [`README.md`](README.md) — the whole idea | [`03-workflows.md`](03-workflows.md) — the YAML spec |
| [`01-architecture.md`](01-architecture.md) — **the twelve laws**, the components, the architecture tests | [`04-agents.md`](04-agents.md) — launching agent CLIs, typed output, sessions |
| [`11-limitations.md`](11-limitations.md) — what this cannot do, and the threat model | [`05-gates.md`](05-gates.md) — gates, the constitution, policy, effects |
| [`12-build-plan.md`](12-build-plan.md) — the build order and the first milestone | [`06-durability.md`](06-durability.md) — the event store, recovery, workspaces, fork |
| | [`02-config.md`](02-config.md) · [`09-cli-and-tui.md`](09-cli-and-tui.md) · [`08-triggers.md`](08-triggers.md) |

**The laws in `01-architecture.md` are not background reading.** L4′ (one execution chokepoint), L6′
(execution is confined and recorded), L8 (typed contracts), L9 (nothing is a special case), and L15
(Kairos never invents work) each forbid a specific shortcut you will be tempted by within the first week.
L15 is the newest and the easiest to breach by accident: a helper that starts a Run without a trigger, a
retry loop that synthesises one, or a "while we're here" background sweep all violate it.

Build documents (`L00`–`L20` in the build plan) **do not exist yet**. When you write one, it gets all
ten sections named in §9 — including the ones that add nothing, which say so and why.

## 1. Language and toolchain

- **Go for everything**: the engine, the executor, the CLI, the TUI. Latest stable release. Standard
  library first.
- **One binary.** `kairos` is the daemon, the executor, the CLI, and the TUI. There is no agent to
  install, no second process to keep in sync. A feature requiring a second process requires an ADR.
- **Embedded SQLite (WAL) is the only datastore**, at `~/.kairos/kairos.db`. No external database, no
  server to run, no connection string.
- **The binary MUST build with `CGO_ENABLED=0` for darwin and linux, arm64 and amd64.** This is a hard
  constraint, not a preference — it is why the SQLite driver is what it is, and CI checks it on every
  commit. A tool whose entire pitch is "one binary" cannot require a C toolchain to cross-compile.
- **HTTP/JSON over a unix socket** for the API. No gRPC, no protobuf, no mTLS, no TLS, no tokens —
  there is no network boundary left to protect. Auth is filesystem permissions plus a peer-credential
  check.
- **No daemon dependency in core.** Docker, Firecracker, and Kubernetes are not runtimes here. They are
  ordinary tools a `shell` actor may invoke, and their absence is *reported* by `kairos doctor`, never
  worked around.

Approved dependencies, and nothing else without an ADR:

| Purpose | Library |
| --- | --- |
| SQLite driver | **`modernc.org/sqlite`** — pure Go. See below. |
| HTTP router | `net/http` (Go 1.22+ routing). No framework. |
| CLI | `github.com/spf13/cobra` |
| Config | `github.com/spf13/viper` — env and file only, no remote config |
| Structured logging | `log/slog` |
| JSON Schema | `github.com/santhosh-tekuri/jsonschema/v6` (draft 2020-12) |
| YAML | `sigs.k8s.io/yaml` (JSON-tag compatible) |
| ULID | `github.com/oklog/ulid/v2` |
| TUI | `github.com/charmbracelet/bubbletea`, `github.com/charmbracelet/lipgloss` |
| Syntax highlighting | `github.com/alecthomas/chroma/v2` — **server-side only**, for the web diff viewer. Rendering to spans in Go means no client-side highlighter, no 400 KB of JS, and no CSP exception. The alternative is unhighlighted diffs, which materially weakens the surface. |
| Compression | `github.com/klauspost/compress/zstd` |
| Process groups | `golang.org/x/sys/unix` — **importable only by `internal/executor/local`** |
| OAuth token dance | `golang.org/x/oauth2` — for the Gmail/calendar connectors only. Everything else about those connectors is plain `net/http`; there is no Google SDK, and there does not need to be. |
| Import-graph tests | `golang.org/x/tools/go/packages` (test-only) |
| Assertions | stdlib. `github.com/google/go-cmp` for diffs. No testify. |

**Why `modernc.org/sqlite` and not `mattn/go-sqlite3`.** `CGO_ENABLED=0 go build` for four platforms
from one machine, in one `make release`, with no clang and no cross-toolchain; `go install …@latest`
works on a machine with no C compiler; `-race` output on a goroutine-heavy event bus stays readable.
The cost is real and accepted: a C-to-Go translation, roughly 1.5–3× slower on writes and ~8–10 MB of
binary. It does not matter, because the write volume is one user's events — three orders of magnitude
below where it would. Mitigations: the driver is imported from **exactly one file**, guarded by
`TestArchitecture_noSQLOutsideStore`, so switching behind a build tag is a one-file change; and
`BenchmarkAppendIf_singleEvent` is a CI gate at `< 5ms p99`. Revisit if that p99 exceeds 10ms
sustained.

## 2. Repository layout

Create only what your document tells you to create.

```text
cmd/kairos/main.go          # THE binary. serve | do | run | ls | show | inbox | doctor | …
internal/
  cli/                      # cobra verbs
    chat/                   # the TUI command
  tui/                      # bubbletea primitives
  config/  version/
  domain/                   # pure types + state machines. Zero I/O. The dependency sink.
    expr/  schema/
  events/                   # envelope, registry, schemas/, fixtures/, redact
  eventstore/               # Store iface, sqlite impl, bus, projections, replay
  engine/                   # advance loop, dispatch, reconcile, timers
  admission/                # pools, claims, leases, budgets, queue
  executor/
    local/                  # THE ONLY os/exec site in the repository
    exectest/               # compliance suite + fake
  workspace/                # mirrors, --reference clones, CoW, snapshots, GC
  artifact/                 # content-addressed fs store, collect, materialise, GC
  constraint/ effect/ policy/ humanqueue/ tasksource/ transport/ reaper/ doctor/
  registry/                 # projects, definitions, actors, DOMAIN PROFILES — parse and validate
                            #   domains.go lives HERE, not in internal/domain/ — see the rule below
  connector/                # email, telegram, calendar — TaskSource + Effect implementations
  api/                      # net/http handlers. A leaf.
  store/sqlite/             # Open, Migrate, migrations/*.sql (embed.FS)
  archtest/
web/                        # the localhost page: html/template + embed.FS + vendored htmx. No Node.
pkg/                        # ships EMPTY. See §7.
```

Hard rules, each enforced by a test in `internal/archtest`:

- **`internal/domain` imports nothing from `internal/`.** It also imports no `path/filepath`: paths are
  infrastructure, and a domain that knows about paths stops replaying identically.
- **Two different things are called "domain", and conflating them will cost you a day.**
  `internal/domain` is the **pure domain model** — types and state machines, zero I/O, the dependency
  sink. A **Domain profile** ([`13-domains.md`](13-domains.md)) is *registry data* that adapts the engine
  to a class of work (code, inbox, messaging), and it lives in `internal/registry/domains.go`. A profile
  is parsed, validated, and versioned like a workflow definition; it is never a package, never imported by
  the engine directly, and never allowed to reach `internal/domain`.
- **Only `internal/executor/local` may import `os/exec`, `syscall`, or `golang.org/x/sys`.** Every child
  process in this system is born in one package, so that every child is recorded before it exists and
  killable from the event log alone. `internal/workspace` runs `git` **through the executor**, not
  through `exec.Command`.
- **Only `internal/store/sqlite` may import `database/sql` or the driver.**
- **Only `cmd/kairos/main.go` may call `os.Exit` or `log.Fatal`.** In one process, a stray `Fatal` in a
  handler kills a Run mid-dispatch.
- **Nothing imports `internal/api`.** The API is a leaf.
- **`internal/tui` and `internal/cli/chat` import neither `os/exec` nor `internal/executor/*`**, and
  import the API client rather than the engine. In the design this was reduced from, that boundary was
  also a *network* boundary; here `TestArchitecture_tuiHasNoExecution` is the only thing holding it, and
  the first person who wants a "just for benchmarking" shortcut will be right that it works. **Treat a
  request to weaken it as a request to delete the boundary.**
- No `util`, `common`, `helpers`, or `misc` package. Ever.

## 3. Style

- Accept interfaces, return structs.
- Constructors take explicit dependencies. No package-level mutable state, no `init()` side effects, no
  global singletons — including loggers and DB handles.
- Every exported symbol has a doc comment saying **why**, not what.
- Errors: wrap with `fmt.Errorf("doing x: %w", err)`. Sentinel errors live in the package owning the
  concept. Never `panic` outside `main`, and never `_ = err` unless the line above explains why.
- Contexts first, always propagated, never stored in a struct.
- Table-driven tests. Names read as sentences: `TestAdvance_pausesRunWhenNodeWaitsOnHuman`.
- No comment restates code. Comments explain invariants, non-obvious ordering, and the ADR that
  justifies the shape.

## 4. Non-negotiable behavioural rules

1. **No silent failure.** Never swallow an error to keep a happy path. A fallback emits an event and a
   WARN log naming what failed and what was substituted.
2. **No orphaned resources.** Every child process, run directory, workspace, and snapshot is recorded
   **before** it is created, with an owner, and is destroyable from the event log alone with no
   in-memory state. A recorded process is identified by `(bootID, pgid, startTime)` — **never by pid
   alone**, because pids are reused and a stale pgid after a reboot belongs to a stranger. Every child
   is started with `Setpgid: true` and killed as a group.
3. **Idempotency.** Every operation the engine performs on the executor, the workspace manager, or the
   artifact store is safe to retry and records its result, so a replay returns the recorded result
   **without acting**. The engine re-dispatches after every crash; re-dispatch must be free.
4. **Determinism at the boundary.** `internal/domain` decision functions are pure: same state plus same
   event yields the same result. No clock reads, no randomness, no I/O. Time and IDs are injected.
5. **Typed contracts.** Node output is JSON-Schema-validated at the boundary, in production, always.
   Never pass prose between nodes. An LLM returning malformed output **fails its node**; it does not
   corrupt the next one, and it is never coerced into shape.
6. **Backwards compatibility of events.** An event type, once merged, is append-only. Add fields; never
   remove or repurpose. New semantics means a new type and an upcaster. Every type ships a schema and a
   fixture per version, and every fixture ever shipped must still project — that test is what makes
   replay and fork work in year three.
7. **The host is the sandbox, and there isn't one.** There is no isolation boundary between an actor and
   this machine. Every capability the `kairos` process has, an LLM actor effectively has. Therefore:
   policy and constraints are **detection and refusal, not containment**; a control that cannot be
   enforced is advertised as **absent** and fails publish rather than being accepted silently; and
   `kairos serve` refuses to run as root. **Never write a comment, doc line, or log message implying
   containment.** New gaps get registered under §8.

## 5. Definition of done

Report partial completion honestly. Never mark done what is not done.

- [ ] Every acceptance criterion in your build document is satisfied, verbatim.
- [ ] `go build ./...`, `go vet ./...`, and `CGO_ENABLED=0` cross-builds for darwin and linux
      (arm64 + amd64) are clean.
- [ ] `golangci-lint run` clean with the repo config.
- [ ] `make arch` passes, **and fails when its violation fixture is enabled**. An architecture test
      nobody has seen fail is a test nobody knows works.
- [ ] Unit tests cover state-machine transitions and error paths, not just the happy path, and pass
      under `-race`.
- [ ] Integration tests pass against a **real SQLite database in `t.TempDir()`** and, where they execute
      anything, against a **fixture `PATH`** built by the test — never the ambient one. There are no
      containers. *An integration test that reads the ambient `PATH` is a flaky test*: the risk moved
      from "is Postgres real" to "is the developer's `git` 2.39 or 2.45".
- [ ] A document is updated if implementation proved the design wrong — plus an ADR recording why.
- [ ] No TODO, FIXME, or commented-out code in the diff.
- [ ] The commit message names the document it satisfies.

## 6. When you disagree with the design

Write an ADR in `adr/NNNN-short-title.md` with status `Proposed`, stating the decision you would make,
its consequences, and the alternatives you rejected. Then implement the existing design, or stop and
ask. **Do not implement your own design and document it afterwards.**

## 7. Scope discipline

Implement exactly the document in front of you. Do not implement the next one because it looked easy.
Do not refactor code another document owns. Do not add features a later phase owns; note the gap in
that document's *Future work* and move on.

Two specific temptations, both of which have already been decided:

- **The deleted *fleet* is not future work.** Do not add a Postgres driver, a gRPC surface, or a
  scheduler "behind an interface, just in case." `01-architecture.md` and `11-limitations.md` state that
  placement, scoring, affinity, preemption, and capacity planning are **foreclosed, not deferred**.
  A remote **runner** is a different thing and is specified in [`07-runners.md`](07-runners.md) as
  permitted work in **phase 4**: one more `Runner` implementation plus a label match. What stays
  forbidden is the scheduler — placement, scoring, migration, or any per-runner divergence in the event
  model. If you find yourself adding a scoring function, stop and write an ADR.
- **`pkg/` ships empty.** There are no third-party integrators to keep compatible, and every exported
  symbol is a promise you pay for later. `internal/` → `pkg/` is a one-commit move when someone
  actually needs it; the reverse is not.

## 8. Limitations must be registered, not merely mentioned

If implementation reveals a limitation — something the system cannot do, does lossily, or does not
defend against — add an entry to [`11-limitations.md`](11-limitations.md) **in the same change**. A
paragraph in the file where you found it is not enough: a limitation findable only by grep will not be
found by the person who needed it.

An entry needs: what it is stated **as a consequence, not a caveat**; blast radius; mitigations each
marked **shipped** / **planned** / **none**; a **Detection** line; and a revisit condition.

Two rules that keep the register honest:

- **A mitigation marked `shipped` must have an enforcing test.** If you cannot write the test, mark it
  `planned` or `none`. An unenforced mitigation is a claim, and a register full of claims is worse than
  no register.
- **Never delete an entry to make the system look better.** Supersede it with the change that resolved
  it. The register is a record, not marketing.

## 9. The tests that must stay green

Nine architecture tests, specified in [`01-architecture.md`](01-architecture.md#architecture-tests):

```
TestArchitecture_domainPurity                  TestArchitecture_noSQLOutsideStore
TestArchitecture_noExecOutsideExecutor         TestArchitecture_processesRecordedBeforeSpawn
TestArchitecture_tuiHasNoExecution             TestArchitecture_noOsExitOutsideMain
TestArchitecture_dependencyDirection           TestArchitecture_agentSocketRouteSubset
TestArchitecture_singleWriter
```

Plus the five that carry the product's central claims, and which no refactor may weaken:

```
TestEngine_survivesKillMidRun            SIGKILL the daemon mid-agent; the Run still completes
TestEngine_ctrlCInterruptsThenResumes    Ctrl-C records the interruption BEFORE exiting
TestEvents_allHistoricalFixturesProject  every fixture ever shipped still upcasts and projects
TestReplay_matchesProjection             replay folds to the same state, or durability is a word
TestUI_everyCallHasCLICounterpart        every web/TUI call maps to an API op AND a CLI verb
TestEngine_everyRunHasATraceableTrigger  L15 — no Run exists that nobody asked for
```

`TestUI_everyCallHasCLICounterpart` is what keeps the two surfaces at parity and keeps neither of them
privileged: **if a UI can do it, `curl` can, and `kairos` does.** It walks the route table and the TUI's
client calls, asserting each maps to a declared API operation with a CLI verb that covers it. Without it,
one surface grows a private endpoint and parity quietly becomes a claim.

`12-build-plan.md` specifies the first two in full, including the step that asserts the *mess* exists
before asserting recovery — a recovery test that would pass against a system with no orphans to reap is
testing nothing.

**Do not weaken a test to make a change pass.** If a test is wrong, fix it in its own change with the
reasoning recorded, and run it against the whole tree first.

### Build documents

Each `L00`–`L20` document has ten sections: **Depends on · Scope (in/out) · Public interfaces · Files to
create · Data changes · Acceptance criteria · Tests · Benchmarks · Migration · Future work.** Include
every one, including those that add nothing — say so and why, rather than omitting the section. A
missing section reads as an oversight; an explicit "none, because…" reads as a decision.
