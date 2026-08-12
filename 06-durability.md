# 06 — Durability

What makes a run survive `Ctrl-C`, a crash, a reboot, and a four-day wait — and what honestly does not.

---

## The event store

```sql
-- ~/.kairos/kairos.db
PRAGMA journal_mode = WAL;
PRAGMA synchronous  = FULL;      -- ~1ms per append; see the guarantee below
PRAGMA fullfsync    = 1;         -- Darwin: without this, "FULL" lies on Apple SSDs
PRAGMA foreign_keys = ON;        -- PER CONNECTION. Off by default. Forget it and FKs do nothing.
PRAGMA busy_timeout = 5000;
PRAGMA temp_store   = MEMORY;
PRAGMA cache_size   = -65536;    -- 64 MiB

CREATE TABLE events (
  global_seq     INTEGER PRIMARY KEY AUTOINCREMENT,
  stream_id      TEXT    NOT NULL,
  sequence       INTEGER NOT NULL,          -- per-stream, gapless, from 1
  event_type     TEXT    NOT NULL,
  event_version  INTEGER NOT NULL DEFAULT 1,
  occurred_at    TEXT    NOT NULL,          -- RFC3339 UTC, fixed width
  recorded_at    TEXT    NOT NULL,
  actor          TEXT    NOT NULL,
  causation_seq  INTEGER,
  correlation_id TEXT    NOT NULL,
  payload        TEXT    NOT NULL,
  UNIQUE (stream_id, sequence),              -- the concurrency control
  CHECK (json_valid(payload)),
  CHECK (length(payload) <= 65536)           -- 64 KiB. Anything larger is an artifact.
) STRICT;

CREATE INDEX events_type_time   ON events (event_type, global_seq DESC);
CREATE INDEX events_correlation ON events (correlation_id, global_seq);
```

Three choices that look like details and are not:

**`AUTOINCREMENT` is mandatory.** A bare `INTEGER PRIMARY KEY` is the rowid, and SQLite *reuses* rowids
below the current max after deletes. Archival deletes the tail of `events`. Without `AUTOINCREMENT` a
`global_seq` gets reused, and every SSE cursor, `causation_seq`, and projection offset in the system
silently points at the wrong event.

**`_txlock=immediate` on the writer connection.** SQLite's default deferred transaction takes a read
lock first and upgrades on first write, and an upgrade under contention returns `SQLITE_BUSY`
*immediately* regardless of `busy_timeout`. This is the SQLite bug everybody ships once.

**`STRICT` tables everywhere**, which turns SQLite's type affinity from a footgun into declared types at
no cost.

### One writer

```go
type Store struct {
    w    *sql.DB   // SetMaxOpenConns(1), _txlock=immediate — THE writer
    r    *sql.DB   // SetMaxOpenConns(8), WAL readers, never blocked
    reqs chan appendReq
}
```

All writes funnel through one goroutine. Consequences:

- `SQLITE_BUSY` is unreachable on the write path.
- **Group commit**: the writer drains up to 64 pending appends or waits 2ms, then commits them in one
  transaction — one `fsync` amortised across a burst. A 12-child fan-out is one fsync, not twelve.
- The CAS is a **single statement**, so there is no read-then-write window even hypothetically:

```sql
INSERT INTO events (stream_id, sequence, …)
SELECT :stream, :expected + 1, …
WHERE (SELECT COALESCE(MAX(sequence), 0) FROM events WHERE stream_id = :stream) = :expected;
-- RowsAffected() == 0 → ErrConflict; the caller re-reads and retries (max 5).
```

A conflict on a run stream is rare by construction, since engine events are routed to a fixed shard
goroutine by `hash(runID)`. Keep the CAS and keep the metric anyway: **a nonzero conflict rate on a
single-process system means something is bypassing the shard router**, which is a bug you want to hear
about.

### Projections apply in the same transaction as the append

```go
type Projection interface {
    Name() string
    Version() int                                       // bump → automatic rebuild at boot
    Apply(ctx context.Context, tx *sql.Tx, ev Event) error  // pure fold, NO I/O
    Reset(ctx context.Context, tx *sql.Tx) error
}
```

This is a genuine simplification the local variant earns: projection lag is structurally zero, the API
has read-your-writes, and the "cold projection → replay on the hot path" branch disappears. The cost is
that a projection bug aborts an append — hence the pure-fold rule and a lint against I/O in `Apply`.

Losing the cold-replay path means replay stops being exercised *by accident*, so it must be exercised on
purpose: recovery uses it, `kairos verify` runs it over every retained run, fork depends on it, and CI
runs it against a corpus of recorded runs.

### Logs are not in the database

`~/.kairos/work/<run>/<nodeExec>/{stdout,stderr}.log`, append-only, `zstd` on rotation at 64 MiB, with
an index row in SQLite. Retention is `rm -rf`.

The original design partitioned a `log_chunks` table by day so retention was `DROP PARTITION`. SQLite
has no partitions, and a multi-GB blob table in the same file bloats the WAL, ruins `VACUUM`, and makes
the one file you back up unbackupable. Files are better on every axis: you get `tail -f` for free, SSE
serves from the file with a **byte offset** as `Last-Event-ID`, node-end collection becomes a
`rename(2)` into the content-addressed store, and **deleting `logs.db` entirely must leave every run
replayable** — which is a much better test than the partition one.

Backpressure, unchanged in policy: **block first, degrade second, never silently.** The reader blocks on
a full channel, the pipe buffer fills, the child blocks in `write(2)` — an agent emitting 400 MB of log
should be *slowed*, not silently truncated. Blocked >5s flips that stream into a 256 KiB tail ring with
`log.degraded` recorded; past a hard cap, `log.truncated{droppedBytes}`. **Never append a chunk with a
gap in its sequence and no marker** — a gap the reader cannot see is unrecoverable; a marker is
auditable.

### Sizing

~400 events/run × 20 runs/day ≈ 3M rows/year ≈ 2.5 GB with indexes. Enough to care about, and the fix
is **payload discipline**, not retention: cap payloads at 64 KiB and push anything over 8 KiB (diffs,
transcripts, large node outputs) into the artifact store with a reference. Then it is ~300 MB/year and
archival is a nicety.

Backup is `VACUUM INTO ~/.kairos/backups/kairos-<ts>.db` — atomic, hot, consistent, one file, no locks
held long. This command must exist so nobody invents their own: **copying a live `.db` and losing the
`-wal` is the number-one way people lose SQLite data.**

---

## The durability guarantee, stated exactly

With WAL + `synchronous=FULL` + `fullfsync`: **every event whose append call has returned is fsync'd.**
After `SIGINT`, `SIGKILL`, a panic, an OOM kill, or a power cut, the next start reconstructs exactly the
state implied by those events — no less, and nothing that was never appended.

Four honest boundaries:

**1. Decisions are durable; child processes are not.** Work an actor did but never reported — no
progress line, no `output.json` — is not a fact, and that node retries. The mitigation is granularity:
progress flushed per turn, `output.json` written to a temp file then `rename`d, stdout continuously on
disk. **A killed 40-minute agent loses its last turn, not its 40 minutes** — and if the process
survived, it loses nothing at all (see adoption, below).

**2. The filesystem is not committed with the log.** The log is authoritative for *decisions*; the
workspace directory is authoritative for *contents*; they commit separately. After a crash the tree may
be **ahead** of the log — a node wrote files, then died before reporting. Every retry and fork path must
treat the tree as possibly-ahead. This is exactly why snapshots before write nodes and refuse-drift-by-
default are not decoration.

**3. In-flight external effects land in `Unknown`.** Resolved by probe on startup. Non-negotiable.

**4. The single `.db` file is a single point of failure.** No replica, by choice. Mitigated by
`VACUUM INTO` on a daily timer keeping 7, `PRAGMA quick_check` at boot, and `integrity_check` in
`kairos verify`.

---

## Recovery on the next start

```text
 1. flock(~/.kairos/daemon.lock) — refuse to start twice, naming the holder's pid
 2. open the db, PRAGMA quick_check, migrate (fail closed)
 3. verify the event schema registry: every type has a schema and a fixture per shipped version
 4. projection version mismatch → Reset + rebuild (a year of one user's events is seconds)
 5. read bootID; compare to the last recorded one
 6. detect an unclean exit: the last system-stream event is not `engine.stopped`
 7. PROBE REALITY and reconcile — divergence always resolved in favour of reality, always recorded:

    for each NodeExecution the log says is Executing:
      process alive && identity matches   → execution.adopted     (resume tailing; await output.json)
      process dead  && output.json valid  → node.output.received   (late, but true)
      process dead  && no valid output    → execution.lost         → Retrying
      process alive && identity MISMATCH  → PID reuse: SIGKILL the group, execution.lost

    for each Workspace in Provisioning    → workspace.corrupt (a half-clone is worthless) → re-provision
    for each Effect in Attempted with no result
                                          → probe by idempotency key → applied | failed
                                          → unprobeable → effect.unknown; blocks the run reaching
                                            Failed; surfaced in the TUI

    orphan scan: recorded process groups with no owning non-terminal NodeExecution
                                          → SIGTERM the group, grace, SIGKILL, record
    workspace dirs with no owning run     → schedule GC

 8. re-dispatch every RunAdvanced whose commands have no recorded outcome (idempotently)
 9. rebuild admission from the log: recount granted claims for still-Executing nodes; release the rest
10. rearm timers; fire overdue ones as timer.fired{late: true}
11. append engine.started{recoveryReport}
12. start the API listener LAST, then accept attaches
```

Step 8 is why commands are appended *before* dispatch. Step 12 is why a start after a crash does not
half-launch six runs. **Never `rm -rf` a workspace during recovery** — its dirty tree may be the only
copy of an agent's work. Only scratch and per-run `TMPDIR`s are removed, which is why `TMPDIR` is
redirected per run in the first place.

### Process adoption is the highest-value row in that table

An agent process started with its own process group and its output going to **files** survives the
daemon's death. So `execution.adopted` means a restart can pick a 40-minute-old Claude session back up
and keep tailing it, instead of paying for those tokens twice. It only works because stdout is a file —
pipes die with their reader. That is a real design constraint the local variant imposes, not an
optimisation.

### Identity, or you will kill someone else's process

PIDs are reused, and on a busy laptop the wraparound is fast enough to matter. Killing pgid 41233
because a crashed run once used it may kill your editor. So:

- **Compare `bootID` first.** Linux `/proc/sys/kernel/random/boot_id`; Darwin
  `sysctl kern.boottime`. If it changed, every recorded pid is meaningless *and* every child is already
  dead — reaping becomes a no-op and only scratch GC runs. This one comparison eliminates the most
  common case: the user rebooted.
- **Then match start time.** Linux: field 22 of `/proc/<pid>/stat` — and split from the **last** `)`,
  never with `Fields`, because `comm` is unquoted and may contain spaces and parentheses. Darwin:
  `P_starttime` from `kern.proc.pid`.
- **Then match a cookie** exported into the child's environment, readable from
  `/proc/<pid>/environ` or `kern.procargs2`.
- **Require ≥2 of {start time, cookie, argv[0]} plus same-uid before any signal.** On fewer, do not
  kill: record `process.orphan.unverified` and surface it in `kairos doctor`. A false negative leaks one
  process and tells you; a false positive kills a stranger's process tree.

The **cookie sweep** is independent of recorded pids — enumerate same-uid processes, match a kairos
cookie whose instance is stale, kill by pgid. This is what catches double-forked daemons that reparented
away, closing most of the gap the original design left open. Its honest residue: a process that scrubs
its own environ is invisible to the sweep. On Linux, `PR_SET_CHILD_SUBREAPER` plus a delegated cgroup
(`cgroup.kill` atomically kills the subtree) closes that too. **On macOS there is no equivalent and the
sweep is the whole answer** — state the asymmetry rather than papering over it.

The acceptance test, inherited with one word changed: *no orphaned processes after 50 runs, including 10
where **kairos itself** was `SIGKILL`ed mid-flight*, verified by a clean same-uid process listing, on
both platforms.

---

## Workspaces

```text
~/.kairos/
├── mirrors/github.com/acme/backend.git/   bare, gc.auto=0, gc.pruneExpire=never
├── work/run_01J8/
│   ├── repo/          ← git clone --reference; the agent's cwd
│   │   └── .kairos/   ← input.json, output.json, context/
│   ├── home/          ← HOME for every process of this run
│   └── nex_01/        stdout.log stderr.log proc.json
├── snapshots/run_01J8/@pre-implement-1/   CoW clone, or a git ref, or a tar
└── artifacts/blobs/sha256/ab/cd…          content-addressed
```

Provisioning: fetch the mirror → `git clone --reference` → link the CoW caches → run `setup` commands.
Roughly a second for a 200 MB repo with no network. See [`02-config.md`](02-config.md) for why
`--reference` and not `worktree`.

### Copy-on-write, by probe not by assumption

| Platform | Mechanism | Cost |
| --- | --- | --- |
| macOS APFS | `clonefile(2)` on the directory — one recursive syscall | metadata only, ms for a 2 GB tree |
| Linux btrfs (subvolume root) | `btrfs subvolume snapshot` | O(1) |
| Linux btrfs/XFS reflink | `ioctl(FICLONE)` per file + a `mkdir` walk | O(files), no data copy |
| ext4, exFAT, cross-volume | `copy_file_range`, then plain copy | O(bytes) |

**Detection is by probe, not by `statfs`.** Filesystem type does not tell you whether reflink is enabled
— XFS needs `reflink=1` at mkfs time — and a wrong assumption here silently turns milliseconds into
minutes. Probe once at startup, record `storage.detected{backend, probeResult}`, and on a copy-only
filesystem **report the real duration rather than pretending it was instant.**

### What a snapshot *is* now

No VMs, so a snapshot never restores a running process. Two layers, take both where possible, and record
which you got:

**Git-level — always available, semantic, cheap.** Not `git stash`, which mutates a working tree an
agent may be mid-write in. Build the snapshot commit out of band, touching no user-visible state:

```bash
GIT_INDEX_FILE=$tmp/idx git -C $repo add -A
tree=$(GIT_INDEX_FILE=$tmp/idx git -C $repo write-tree)
sha=$(git -C $repo commit-tree $tree -p HEAD -m "kairos @pre-implement-1")
git -C $repo update-ref refs/kairos/runs/<runID>/<seq> $sha
```

Content-addressed and deduplicated across every snapshot of every run forever, so 500 runs cost the
deltas. Restoring is a checkout. And it is **inspectable with tools you already have**:
`git diff refs/kairos/runs/A/5 refs/kairos/runs/A/7`, or
`git log --graph --all --glob='refs/kairos/**'` to see the tree of every attempt any agent ever made on
your repo. That last one is a free and genuinely novel artifact.

**Tree-level — whole workspace, needs CoW.** Captures ignored build state too (`node_modules`,
`target/`, `.venv`). O(1) where supported; a `tar.zst` minus declared caches otherwise, or skipped with
a recorded reason.

```
workspace.snapshot.taken{label: "@pre-implement-1", kind: "git+tree", ref: …, path: …}
workspace.snapshot.taken{label: "@fork-5",          kind: "git", reason: "cow-unsupported"}
```

The trap worth naming: `git clean -fdx` in a `freshWorkspace` retry is how you delete forty minutes of
`npm ci`. Which is exactly why tree snapshots earn their keep and why cache paths must be declared and
excluded.

### GC, and a limit a server did not need

```text
usage > reclaimAt (90% of the filesystem OR maxRootBytes, whichever binds first)
  1. workspaces already Deleting
  2. expired-retention workspaces of terminal runs
  3. snapshots beyond keepSnapshots, unreferenced by the log
  4. cold caches (untouched > 7d)
  5. archived run dirs, logs past retention
  6. workspaces of runs waiting > threshold        ← OFF BY DEFAULT
usage > refuseAt (95%) → refuse admission of anything needing a new workspace
```

`maxRootBytes` (default 100 GiB) and `minFreeAbsolute` (default 20 GiB) exist because this is somebody's
laptop. A dedicated server could fill its disk; a laptop filling its disk breaks your editor, Slack, and
Time Machine at once. **Category 6 is off by default** — on a server, reclaiming a workspace during a
three-day wait was necessary; locally it trades your uncommitted work for disk you probably have.

**Filesystem quotas die**, and this is a genuine regression: a runaway `docker build` can fill your disk
and the original design's argument for kernel-enforced quotas ("a 30-second poll is too slow") was
right. What remains: a 5-second disk watchdog that fails the node, `SIGSTOP` on the group at a hard
threshold so you can intervene rather than losing the machine, and opt-in cgroup `io.max`/`memory.max`
on Linux. On macOS, nothing. Say so.

---

## Fork and replay

**Replay is unaffected by going local, and it gets cheaper.** It folds *recorded facts*; it never
re-invokes an actor or re-performs an effect. The whole run stream is contiguous rows in a
memory-mapped local file, so replaying a 400-event run is one query and a fold.

**Fork gets cheaper too, and in one way better.** Copy events `1..N` into a new run stream tagged
`forkedFrom`, restore the workspace to the same point, append `run.forked{fromRun, atSequence,
overrides}`, continue from node `N+1`. The prefix is copied, not shared by reference — kilobytes, and it
keeps `(stream, sequence)` a total order.

### What is restorable, precisely

| | Restorable? | Mechanism and limit |
| --- | --- | --- |
| **Event log prefix** | **Exactly** | Rows. Carries params, definition ref, every node input and output, every decision, every finding, attempt history, cost. |
| **Node inputs/outputs** | **Exactly** | They are recorded facts, not recomputations. |
| **Workspace files** | **Approximately, if snapshotted** | Tracked + untracked-non-ignored exactly. **Not**: gitignored build state, file mtimes (so build systems may rebuild), empty directories, xattrs, anything outside the workspace root. |
| **Agent session** | **No** | You cannot rewind a model conversation to turn 14 of 41. After a compaction the pre-compaction context is gone even for the original run. Every fork cold-starts a new session seeded from a context digest — which as a bonus deletes the original design's guest-clock-stepping and network-identity problems entirely. |
| **External effects already applied** | **No, ever** | The fork inherits none. `idempotencyScope: lineage` derives the key from the lineage root, so `gh.pr.create` on a fork **updates** the original PR rather than opening a second. Genuinely non-idempotent effects — a Slack post, an email — happen twice, and the CLI must warn, naming them, before forking. |

**Refuse drift by default.** If the requested snapshot ref does not exist, `Fork` returns
`ErrWorkspaceDrift` and **creates no run**. `--allow-drift` snapshots now and records
`fork.workspace.drifted{requestedSeq, actualSeq}`, and `kairos compare` carries the annotation into its
output. The reasoning is the sharpest in the whole corpus: a fork whose filesystem silently came from a
different moment gets read as a **model** difference when it is a **state** difference — a wrong
conclusion drawn from an experiment that looked clean.

**One way local is strictly better: the fork window becomes unbounded.** Git objects are cheap and
content-addressed, so you can fork a run from three months ago. The original design's snapshot-retention
window and its CLI warning both disappear.

### The honest statement

> A local fork restores the run's **reasoning** exactly and its **filesystem** approximately. Exactly:
> every event, node input and output, human decision, finding, definition version, and cost — because
> these are recorded facts. Approximately: the workspace comes from the last snapshot at a node
> boundary, excluding gitignored build state and mtimes; with no snapshot at the requested sequence the
> fork is **refused**, not silently drifted. Not at all: the agent's session, and any side effect
> already committed to a system you do not own. A fork is a valid controlled experiment about actors,
> inputs, and definitions given identical recorded upstream state. It is not a time machine, and it does
> not resume a train of thought.

`kairos verify` runs replay over all retained runs and asserts the fold matches the projection. Keep the
deliberately-injected-impurity test that proves it has teeth: without it, "the domain is deterministic"
is a comment.

---

## Preflight: the host is the image

"Assume the tooling is present" is a fine *runtime* assumption and a terrible *startup* one. Every
failure it hides surfaces forty minutes and eight dollars into a run, as a confusing exit code from a
tool that was never there.

`kairos doctor` probes, in parallel, in ~200ms:

| Group | Probes |
| --- | --- |
| hard core | `git ≥2.39`, `/bin/sh`, writable state and workspace roots |
| forge | `gh ≥2.40` **and `gh auth status`** — presence is not authorisation, and conflating them is the most common false green in this whole area; `git config user.email` set |
| agents | every `runner.command` a published actor names, each with an auth probe (`~/.claude/.credentials.json` or `ANTHROPIC_API_KEY`, …) |
| toolchains | only what a published workflow declares: `go`/`golangci-lint`, `node`/`npm`/`eslint`, `python3`/`ruff`/`pytest`, `cargo`/`clippy`, `make`, `jq`, `rg` |
| browser | `npx playwright --version` **and installed browsers** |
| non-tool | free space vs `refuseAt` and `minFreeAbsolute`; **`RLIMIT_NOFILE` ≥ 8192**; `TMPDIR` writable and on the same filesystem as the workspace root; macOS `xcode-select -p`; is the workspace root inside iCloud/Dropbox or on a case-insensitive volume; Linux cgroup delegation and `bwrap`; clock skew |

`RLIMIT_NOFILE` is the highest-yield macOS check: the default soft limit is 256, and `node`, `go test
-p`, and `webpack` blow straight through it, producing `EMFILE` failures that read as flaky tests. Raise
it in the parent at startup so children inherit — Go's `os/exec` has no rlimit hook.

The rule inherited from the original design, and it transfers word for word: **never conclude a tool
works because it is on `PATH` — execute it.** A `golangci-lint` shim that exits 127 reports `Broken`,
not `OK`.

Resolution happens against a **captured** PATH, once, into an immutable snapshot recorded as an event.
Never `exec.LookPath` at call time — a PATH change mid-run must not silently change which `go` compiles.
Per node execution, `stat` the resolved path and compare mtime+size; a mismatch records
`toolchain.drift{tool, was, now}`, fails that node retryably, and refuses new claims needing that tool
until doctor re-probes.

Workflows declare what they need, which is the direct replacement for the deleted node-capability
labels:

```yaml
spec:
  requires: [go >= 1.24, golangci-lint, gh >= 2.40]
  nodes:
    - id: e2e
      requires: [node, "playwright:chromium"]        # the binary must exist
      resources: { capabilities: ["browser:playwright"] }   # AND a slot must be free
```

Note the deliberate split on that last node: a tool and a slot are different scarcities, and the old
design conflated them because one machine label covered both.

**Publish time vs run time**, split by whether the problem is an authoring bug or an environment fact:

- unknown tool, unparseable constraint, unused requirement → **error**. These are typos, unfixable by
  installing anything.
- known tool absent or too old on this host → **warning**, recorded in the publish event; **error under
  `--strict`**, which is what CI uses. Publishing a workflow on a laptop that lacks the Android SDK,
  intending to install it tomorrow, must not be blocked — a single host's `PATH` is not an authoritative
  registry.
- **at run start, before any node executes**: any hard requirement unsatisfied → the run **never enters
  Running**. `run.rejected{reason: unsatisfied-requirements}`, non-zero exit, with the `Fix` lines
  printed. A request nothing can ever satisfy fails immediately with a clear message rather than sitting
  in a queue being retried until the wall clock expires.
