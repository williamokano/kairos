# L18 — Fork, replay verify, compare

## Depends on

L02 (event store), L06 (local executor + workspaces), transitively L00-L01. Confirmed the correct
next document per `12-build-plan.md`'s numbering-as-sequence rule once L00-L14, L16, and L17 were
complete — the only remaining low-numbered node. This document also unblocks L15 (TUI), whose sole
remaining dependency it was.

## Scope

**In.**
- Git-ref workspace snapshots (ADR 0006 layer 1): `internal/workspace.Manager.SnapshotGitRef`
  builds an out-of-band commit via a throwaway `GIT_INDEX_FILE` (`git add -A` → `write-tree` →
  `commit-tree` → `update-ref refs/kairos/runs/<runID>/<seq>`), touching no user-visible state —
  never `git stash`. `internal/engine.maybeSnapshotWorkspace` takes one automatically at every
  successful `workspace: write` node's completion boundary (`actor_shell.go`/`actor_llm.go`),
  recording `WorkspaceSnapshotTaken`.
- Copy-on-write tree probing (ADR 0006 layer 2): `internal/executor/local.ProbeCoWSupport`/
  `CloneTree` — real `FICLONE` ioctl probe and per-file reflink clone on Linux (detection is by
  probe, never by filesystem type), a `tar.zst` fallback (`internal/workspace.SnapshotTree`)
  everywhere else. Lives in `internal/executor/local`, not `internal/workspace`, because a raw
  ioctl is exactly the class of OS-level operation the one-execution-chokepoint law confines there
  (see Documented decisions #1 — a real architecture-test violation this document caught and
  fixed).
- `Engine.Fork`: copies a source run's event prefix `1..AtSequence` into a brand-new run stream,
  restores the workspace (git-ref layer only — see Documented decisions #6) from the nearest exact
  snapshot, applies `--set k=v` overrides to the copied `TriggerReceived`'s params, and appends
  `run.forked`. Refuses by default (`ErrWorkspaceDrift`) when no exact snapshot exists at the
  requested sequence; `--allow-drift` takes a fresh live snapshot instead and records
  `fork.workspace.drifted`.
- `shard.primeForked`/`primedUpTo`: the mechanism that lets a fork's copied event prefix be folded
  and dispatched exactly once (by `Fork` itself, synchronously) without the live engine loop
  re-executing every already-completed node a second time when those same copied events arrive on
  its ordinary `Store.Subscribe` path.
- `internal/effect.IdempotencyKey` now derives from a run's lineage root (`Engine.lineageRootFor`,
  walking a run's own `RunForked` event, if any) instead of its own run id — L12's placeholder
  ("lineage is scoped to RunID until fork gives it real meaning") made real: a fork's effect
  actions update the lineage's already-applied external state rather than duplicating it.
- `TestReplay_matchesProjection` made real — one of `AGENTS.md` §9's five original product-central-
  claim tests, named since L00, never implemented until now. Extends `Store.Verify` (L02/L05/L06)
  with a real multi-run corpus test and a deliberately-injected-impurity companion that corrupts
  `run_state_projection` directly and confirms `Verify` catches it.
- `kairos fork <runID> [--at <seq>] [--set k=v ...] [--allow-drift]` and
  `kairos compare <runA> <runB>` CLI verbs, `POST /runs/{id}/fork` and
  `GET /runs/{a}/compare/{b}` API routes, kept passing `TestUI_everyCallHasCLICounterpart`.

**Out.** The debugger (breakpoints, step, variable injection) — see Documented decisions #5 and
NL-47; CoW tree-level restore in `Fork`'s own workspace-restore path (the capability exists and is
tested, just not yet wired into `Fork` — NL-46); TUI/web rendering of any of this (L15/L20, later
documents); cost in `kairos compare` (NL-48, downstream of NL-30's never-metered-spend gap).

## Documented decisions

1. **CoW probing lives in `internal/executor/local`, not `internal/workspace`.** The first
   implementation put `golang.org/x/sys/unix.IoctlFileClone` directly in `internal/workspace`,
   which `TestArchitecture_noExecOutsideExecutor` correctly failed: a raw ioctl is exactly the
   class of OS-level operation the one-execution-chokepoint law confines to
   `internal/executor/local`/`internal/executor/exectest`/`cmd/kairos`. Moved `ProbeCoWSupport`/
   `CloneTree` there (mirroring the existing `bootid_linux.go`/`bootid_darwin.go` build-tag
   pattern); `internal/workspace.SnapshotTree` calls them, never touching `x/sys/unix` itself.
2. **`Fork`'s copy boundary is the requested `AtSequence`; the workspace source may come from a
   later, live moment under `--allow-drift`.** 06-durability.md's exact framing — "a fork whose
   filesystem silently came from a different moment gets read as a model difference when it is a
   state difference" — is only meaningful if the REASONING cutoff and the WORKSPACE moment can
   diverge and be named separately. `ForkResult.ActualSnapshotSeq` reports where the workspace
   actually came from; `RequestedSeq` (recorded on `ForkWorkspaceDrifted`) is what the caller
   asked for.
3. **"The last copied event's Cmds" is wrong; "the last copied event that produced any Cmds" is
   right — a real bug this document's own tests caught.** A copy boundary chosen at a node's
   completion snapshot lands, by construction, on `WorkspaceSnapshotTaken` — a bookkeeping no-op
   fold with no Cmds — one event AFTER the `NodeOutputReceived` that actually produced the routing
   decision (`CmdStartNode` for the next node). Folding strictly "the very last event" silently
   lost that Cmd and stalled the forked run forever at the next node's `Pending` state, undetected
   until the fork's own end-to-end test polled past its deadline. Fixed by tracking the most
   recent NON-EMPTY Cmds slice across the whole replay, not simply the final iteration's.
4. **`shard.primeForked` is a synchronous, channel-delivered message to the run's own shard
   goroutine — not a direct write to `shard.states`.** `Engine.Start`'s live loop is always running
   by the time `Fork` executes (a real daemon, per every prior document's assumption); writing
   `shard.states[newRunID]` from `Fork`'s own goroutine would race the shard's single-goroutine-
   owns-its-state invariant every other part of this engine already depends on
   (`shard.go`'s doc comment). A dedicated `primeCh` + `done` channel keeps that invariant intact
   while still guaranteeing the prime is visible before `Fork` appends anything to the store (the
   ordering that makes `primedUpTo`'s skip-check race-free).
5. **The debugger is not built.** `12-build-plan.md` names it only in one prose sentence with no
   dedicated interaction-model specification anywhere in the corpus — building a speculative event/
   API surface for an unspecified mechanism is exactly the guessing AGENTS §4 rule 1 forbids.
   Registered honestly as NL-47, with `kairos fork --at <seq> --set k=v` named as today's coarse
   substitute (retry with a different input, at node-boundary granularity).
6. **`Fork`'s workspace restore uses ADR 0006's git-ref layer only, never the CoW tree layer.**
   `SnapshotTree`/`ProbeCoWSupport` are real and tested independently, but wiring tree-level
   capture into the node-boundary snapshot hook and `Fork`'s own restore path is separate,
   non-trivial engine work with no concrete workflow yet paying its complexity cost. The git-ref
   layer alone already satisfies 06-durability.md's documented "restorable approximately" contract
   (tracked + untracked-non-ignored files, not gitignored build state) — registered as NL-46, not
   silently narrowed.
7. **`kairos compare` computes duration/attempts/findings by walking each run's full event log on
   every call**, rather than maintaining a dedicated summary projection. Two runs' full histories
   are small (kilobytes to low megabytes even for a long run — 06-durability.md's own framing of
   why local replay is cheap), and a summary projection would be new SQL-writer surface for a
   read-only, infrequently-called CLI verb — not worth the complexity this document's scope
   warrants.
8. **`rekeyRunID` rewrites every copied event's `RunID` field via reflection, not a per-type
   switch.** Thirty-plus event types carry a `RunID` field; a hand-written case for each would be
   exactly the parallel machinery AGENTS §7 warns against, and — unlike a switch — automatically
   covers every future event type without this document (or any later one) needing to remember to
   extend it.
9. **New source-repo test fixtures must not share a `t.TempDir()` parent with the test's own
   `workRoot`.** `internal/workspace.checkSafeSource`'s "inside kairos's own state directory"
   refusal compares a source repo's path against `filepath.Dir(workRoot)` — correct for real usage
   (`$KAIROS_HOME/work` vs. an unrelated repo elsewhere on disk) but a false positive when a test
   builds both via sibling `t.TempDir()` calls, which Go's testing package nests under one shared
   per-test parent by design. Not a production bug (documented, not "fixed" in `checkSafeSource`
   itself) — `fork_test.go`'s `newSourceRepoDir` roots its source repo in a separate
   `os.MkdirTemp("", ...)` bucket instead.

## Public interfaces

```go
// internal/workspace
func (m *Manager) SnapshotGitRef(ctx context.Context, ws Workspace, atSeq int) (Snapshot, error)
func (m *Manager) RestoreGitRef(ctx context.Context, dst Workspace, sourceWorkspaceDir string, snap Snapshot) error
func (m *Manager) Existing(runID string) Workspace
func SnapshotTree(srcDir, destDir string, forceFallback bool) (kind, path string, err error)
type Snapshot struct { Ref, SHA string }

// internal/executor/local
func ProbeCoWSupport(dir string) bool
func CloneTree(srcDir, destDir string) error

// internal/engine
type ForkRequest struct { FromRunID string; AtSequence int; Overrides map[string]string; AllowDrift bool }
type ForkResult struct { NewRunID string; Drifted bool; ActualSnapshotSeq int }
var ErrWorkspaceDrift error
func (e *Engine) Fork(ctx context.Context, req ForkRequest) (ForkResult, error)

// internal/domain, additive events
type WorkspaceSnapshotTaken struct { RunID, NodeID, ExecID string; AtSequence int; Label, Kind, Ref, SHA string }
type RunForked struct { RunID, FromRunID, LineageRoot string; AtSequence int; Overrides map[string]string }
type ForkWorkspaceDrifted struct { RunID string; RequestedSeq, ActualSeq int }

// internal/eventstore (unchanged signature, now with a dedicated test)
func (s *store) Verify(ctx context.Context) (VerifyReport, error)
```

## Files to create

```
internal/workspace/snapshot.go  snapshot_test.go
internal/executor/local/cow_linux.go  cow_other.go

internal/engine/fork.go  fork_test.go
internal/eventstore/replay_test.go

internal/api/fork.go  compare.go
internal/cli/fork.go  compare.go

# modified:
internal/domain/event.go  advance.go
internal/events/init.go  registry.go  registry_test.go  fixtures_test.go
internal/events/schemas/workspace.snapshot.taken/v1.json  fixtures/workspace.snapshot.taken/v1.json
internal/events/schemas/run.forked/v1.json  fixtures/run.forked/v1.json
internal/events/schemas/fork.workspace.drifted/v1.json  fixtures/fork.workspace.drifted/v1.json
internal/engine/shard.go  engine.go  actor_shell.go  actor_llm.go
internal/effect/effect.go
internal/api/server.go
internal/apispec/ops.go
internal/cli/root.go  client.go
11-limitations.md
```

## Data changes

None beyond L02's schema — `refs/kairos/runs/<runID>/<seq>` git refs live inside each run's own
provisioned workspace repository, not in `kairos.db`.

## Acceptance criteria

- `go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run` clean; `go test ./... -race`
  green across every package, including the real-binary `cmd/kairos` tests.
- All architecture tests pass, including `TestArchitecture_noExecOutsideExecutor` (the CoW-probe
  relocation this document's own tests caught) and `TestUI_everyCallHasCLICounterpart`.
- `TestReplay_matchesProjection` and its deliberately-injected-impurity companion both pass —
  `AGENTS.md`'s five originally-named product-central-claim tests are now all real.
- `TestEngine_forkCopiesReasoningExactlyAndRestoresWorkspaceApproximately` and
  `TestEngine_forkRefusesDriftByDefault` (`internal/engine`) prove, end to end against a real git
  workspace: exact reasoning restoration (a forked run's already-completed node has exactly one
  recorded execution, never redispatched), approximate workspace restoration (the fork sees
  exactly the tree state at its cutoff, then continues writing its own history), refuse-by-default
  drift, and `--allow-drift`'s recorded `fork.workspace.drifted`.
- A real end-to-end CLI smoke test (`kairos run` → `kairos fork --set k=v` → `kairos show` on the
  new run → `kairos db verify` → `kairos compare` → the drift-refusal and `--allow-drift` paths)
  passes against the real built binary — performed manually during this document's own
  verification, not (yet) a committed `cmd/kairos` test file; see Future work.
- `make cross` builds `CGO_ENABLED=0` for darwin/linux × arm64/amd64.
- No `TODO`, `FIXME`, or commented-out code in the diff.

## Tests

- `internal/workspace/snapshot_test.go`: `TestSnapshotGitRef_capturesCurrentTreeWithoutTouchingWorkingState`,
  `TestSnapshotGitRef_notGitStash`, `TestSnapshotTree_realCloneAndForcedFallbackBothProduceRestoreableCopies`
  (both the natural and forced-fallback paths, so tar.zst is never dead code on a CoW-capable
  host), `TestProbeCoWSupport_realProbeOnThisHost`.
- `internal/engine/fork_test.go`: the two named end-to-end tests above.
- `internal/eventstore/replay_test.go`: `TestReplay_matchesProjection`,
  `TestReplay_matchesProjection_catchesADeliberateImpurity`.

## Benchmarks

None. Git-ref snapshotting is cheap by construction (ADR 0006: "kilobytes, and it keeps `(stream,
sequence)` a total order") and nothing here is on L02's durability-sensitive hot path at a scale
that warrants one yet.

## Migration

None from a prior version.

## Future work

- Wire `SnapshotTree`'s CoW/tar.zst capture into the node-boundary snapshot hook and `Fork`'s
  restore path, so gitignored build state (a warm `node_modules`/`target/`) survives a fork too
  (NL-46).
- The debugger (NL-47), once a concrete breakpoint/step/inject interaction model is specified —
  following this document's event-sourced pattern for whatever that turns out to be.
- Real spend metering (NL-30's own revisit condition) would let `kairos compare` report cost for
  free (NL-48) — the summarization code already walks the full event log per run.
- A committed `cmd/kairos` end-to-end test for `kairos fork`/`kairos compare` against the real
  built binary, mirroring `kill_mid_run_test.go`'s harness — this document's own verification ran
  the equivalent by hand; committing it as a real test is straightforward follow-on work, not
  deferred for a design reason.
- `github`/`jira`/`linear` `TaskSource` providers, real cron(5) syntax, and `repo-watch` remain
  L16's own registered gaps (NL-41, NL-44, NL-45) — unrelated to this document, noted here only
  because `kairos fork`ing a trigger-created run touches the same `TriggerReceived` copying path.
