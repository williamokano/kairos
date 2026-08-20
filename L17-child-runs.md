# L17 — Child runs: spawn, join, Degraded

## Depends on

L07 (admission), L06 (workspaces), transitively L00-L05. Confirmed the correct next document per
`12-build-plan.md`'s numbering-as-sequence rule once L00-L14 and L16 were complete: L18 (fork +
replay verify) needs only L02+L06 and was also unblocked, but L17 is the lower-numbered of the two.

## Scope

**In.**
- `actor: "spawn"` — a new coordinator dispatch kind, alongside `rule`/`shell`/`claude`/`effect`
  from L05/L08/L12. Requires a `spawn:` block; requires a `join:` block; defaults `workspace: none`
  (03-workflows.md: "a coordinator run declares workspace: none, so it costs nothing at all").
- The `spawn:` block: `workflow` (resolved as a sibling file of the coordinator's own definition —
  see Documented decisions #2), `forEach` (a `"$.outputs.<nodeID>.<field>"` reference, resolved at
  runtime for the first time — `checkInputRefs` has only ever statically validated this syntax
  until now), `strategy: bounded(N)` (the only strategy this document implements — a real,
  progressive-refill live-child-count cap, not a one-time cap on how many items `forEach` may
  produce), `inheritWorkspace: clone` (the only mode implemented — see Documented decisions #7).
- The `join:` block: `mode: waitAll` (the only mode implemented), `onChildFailure: fail | degrade`
  (defaults to `fail`).
- `domain.WaitChildRun` made real — L01 reserved this `WaitKind` value from the start; this
  document is its first real consumer, wired through the exact same `CmdEnterWait` +
  companion-cmd pairing `WaitHuman`/`CmdCreateHumanTask` already established (now
  `WaitChildRun`/`CmdSpawnChildren`).
- `RunDegraded`/`RunDegradedResolved` made real — both events and their domain transitions existed
  since (at latest) the L05-era system-stream work as unused placeholders; this document is what
  actually produces and resolves them.
- `maxSpawnDepth` (already a named `LimitsDef` field, default 2) enforced for real at spawn-dispatch
  time.
- Kill-mid-spawn recovery: `Reconcile`'s catch-up pass discovers a coordinator's already-finished
  children even if no live engine ever watched them complete, and (new this document) re-enters
  `dispatchSpawnChildren` for an `ExecWaiting` exec whose plan was never even recorded — the daemon
  crashed between the exec becoming `Waiting` and `CmdSpawnChildren` ever dispatching.

**Out.** The fork+replay-verify+compare+debugger tooling (L18 — a separate 12-day line item, not
built here despite being topically adjacent); the workspace snapshot mechanism (another separate
4-day line item in the same phase); any TUI/web rendering of child-run trees (L15/L20); a real
`git fetch <child> && git merge FETCH_HEAD` integration provider (see Documented decisions #7);
per-item `paths[]` "waves" overlap scheduling (see Documented decisions #9); a named-workflow
registry (see Documented decisions #2).

## Documented decisions

1. **`actor: "spawn"` is required, deviating from `03-workflows.md`'s literal fan-out example**,
   which shows a node with a `spawn:`/`join:` block and no `actor:` field at all. Every other node
   kind in this codebase is dispatched through the actor-keyed switch decision #5 (L03) already
   established as the one required-per-node field; making `actor` optional for spawn nodes would
   have meant special-casing "no actor" through `defaultNode`, `requiresOutputSchema`, and
   `runActorDispatch` for a single node kind. `actor: "spawn"` matches the existing precedent
   `actor: "effect"` (L12) already set for "the node IS the dispatch kind, not a generic actor with
   an extra block." `defaultNode` auto-injects `wait.on: [{kind: child-run}]` and
   `wait.onTimeout: park` when `actor == "spawn"` and no `wait:` block was declared, so the doc's
   terse example still parses and dispatches correctly without the author writing either field.
2. **`spawn.workflow` (a bare name, e.g. `"implement-task"`) resolves to a sibling file of the
   coordinator's own definition** — `<dir-of-parent.yaml>/<workflow>.yaml`. No named-workflow
   registry exists anywhere in this codebase; every other document addresses a definition by
   absolute file path (`kairos run <file>`, `spawn.workflow` in the trigger sources' `flow:`
   front-matter field from L16). A real registry (`kairos workflow register <name> <path>` or
   similar) is Future work, not silently faked.
3. **A resolved `forEach` item becomes the child's `params` under the key `"item"`**
   (`{"item": <resolved-element>}`). `03-workflows.md` does not specify the exact parameter-passing
   convention between a fan-out and its children; this is the narrowest reasonable choice, matching
   the pattern every other builtin actor in this codebase uses for its own fixed output shape.
4. **`resolveForEach` supports exactly one dotted-path shape**:
   `"$.outputs.<nodeID>.<field.path>"`, resolved against the referenced node's most recent
   `NodeOutputReceived`/matched-`NodeWaitResolved` event in the coordinator's own stream — read
   directly from the log, since `domain.NodeExecution` carries no `Output` field (L09's design: an
   actor's output lives only in the event log, referenced by artifact hash when oversized, never
   duplicated into `RunState`). An `OutputRef` (L09's oversized-output blob reference) fails loudly
   rather than being silently treated as empty — `forEach` over an oversized output is a real,
   named gap (Future work), not a partial JSONPath engine covering some inputs and silently
   mishandling others.
5. **`WaitFailed` is a new `domain.WaitOutcome` value**, bumping `node.wait.resolved`'s schema to
   v2 (v1's `Outcome` enum only ever had `matched`/`timed-out`) rather than editing v1's schema in
   place (AGENTS §4 rule 6: an event type's shape, once merged, is append-only). A spawn/join's
   `onChildFailure: fail` resolution is a genuine failure, not a schema problem — squeezing it into
   `SchemaValid: false` would have mislabeled the actual reason. `handleFailureOutcome` (already the
   generic Waiting→Failed path `WaitTimedOut` uses) handles it identically: retry if
   `MaxAttempts` allows, else route via the normal failure edge.
6. **Every dispatch-time spawn failure (no `RunSpawner` configured, a bad `forEach` reference, a
   depth-limit breach, `RunSpawner.SpawnChild` itself erroring) routes through
   `NodeWaitResolved{Outcome: WaitFailed}`, never `NodeExecutionFailed` directly** — a real bug this
   document's own tests caught: by the time `CmdSpawnChildren` dispatches, `CmdEnterWait` has
   already moved the exec to `ExecWaiting` in the same domain fold that produced both commands, and
   `legalExecEvents` only accepts `NodeExecutionFailed` against `ExecExecuting`/`ExecAdopted`, never
   `ExecWaiting`. The specific reason string is logged (`e.log.Error`) rather than threaded through
   the event — neither `NodeWaitResolved` nor `handleFailureOutcome` carries a free-form message
   today (matching `WaitTimedOut`'s existing precedent), and adding one for this single case alone
   was judged not worth a domain-event-shape change.
7. **`inheritWorkspace: clone` requires no new cloning machinery.** Re-reading `03-workflows.md`'s
   "Children get `--reference` clones off the same mirror" against what already exists: a child is
   just an ordinary Run, and any of ITS OWN `workspace: write` nodes already provision a real
   `--reference` clone via `internal/workspace`'s existing L06 machinery, keyed off the SAME
   daemon-wide `WorkspaceRepo`/mirror the parent uses (no per-run repo selection exists yet, an
   already-documented L06/L08/L11/L12 gap this document doesn't change). `inheritWorkspace: clone`
   is validated as the only accepted value and otherwise does no independent work — it's a
   confirmation of existing behavior, not a new code path. The doc's further "integration is
   `git fetch <child> && git merge FETCH_HEAD`" is explicitly **not** built here: no concrete
   trigger for when integration should happen is specified anywhere in the corpus (a coordinator's
   own next node, an automatic post-join step, and a dedicated `git.merge` effect provider are all
   equally plausible readings), so it's registered as Future work rather than guessed at.
8. **`Degraded`'s "resolvable only by a coordinator node" is read narrowly**: only the SAME
   coordinator's own join-completion logic clears a `RunDegraded` it caused — there is no separate
   human/policy resolution path in this document's scope. Concretely: `RunDegraded` is recorded as
   soon as a failure is seen among still-in-flight children (an early, honest signal, not
   retroactively applied only once the whole join finishes); once every planned child is spawned
   and terminal, if the run is still `Degraded`, `RunDegradedResolved` is appended immediately
   before the join's own success is recorded. A workflow currently has no way to leave a Run
   sitting in `Degraded` for a human to inspect and manually resolve — that richer flow (plausible
   future work, distinct from L13's human-decision machinery) is named, not built.
9. **"Waves" (the cheap `paths[]` overlap check) is not implemented.** `03-workflows.md` frames it
   as comparing forEach items' own declared `paths[]` for overlap, but gives no concrete schema for
   how one resolved `forEach` element (today, an arbitrary JSON value under `params.item`) would
   itself carry a `paths[]` — a genuinely underspecified per-item structure this document declines
   to invent. Registered as Future work rather than a partial, guessed-at implementation.
10. **A single engine-wide `spawnMu sync.Mutex` serializes every join's read-decide-act sequence**,
    not per-coordinator-exec locking. Two real races surfaced this document's own tests found: (a)
    the initial `CmdSpawnChildren` dispatch and a same-batch child's near-instant completion racing
    to spawn the same next `bounded(N)` slot (two `TriggerRun` calls for the same dedupe key,
    caught by `tasksource`'s own dedupe-claim detection but surfaced here as a spurious node
    failure rather than the harmless race it actually was); (b) two children finishing
    near-simultaneously both reading a stale "not yet resolved" join state and both attempting to
    resolve or refill it, one succeeding and the second hitting `domain.ErrIllegalTransition`
    trying to append a second `NodeWaitResolved` against an exec that had already moved off
    `ExecWaiting`. A coordinator's join progression is low-frequency relative to the rest of the
    engine's dispatch traffic, so serializing it globally costs nothing worth avoiding — narrower
    per-key locking was judged unnecessary complexity for this document's scope.
11. **`reconcileSpawnJoin`/`dispatchSpawnChildren` follow `resolveConversationWait`
    (L14)/`recoverLost` (L05)'s `dispatchCmds bool` precedent exactly**, not a new pattern: `false`
    from every live call site (the shard already watching the affected run's stream dispatches for
    itself via the normal `Subscribe` loop — dispatching here too would run a cmd twice), `true`
    from every `Reconcile`-time call (no shard exists yet at that point in boot, so a fold's cmds
    are permanently lost unless this call dispatches them itself). This document's own end-to-end
    kill-mid-spawn test is what caught the gap on first pass: calling `Reconcile` alone (matching
    the test's initial, wrong assumption) only ever gets a join as far as `NodeWaitResolved`
    (`Waiting`→`Executing`, dispatching `CmdEvaluateGates`) without the `dispatchCmds` threading —
    finishing requires `Start`'s live loop too, exactly like every other reconciled node, which is
    what production always runs (`Reconcile` then `Start`, never `Reconcile` alone).

## Public interfaces

```go
// internal/engine
type SpawnChildRequest struct {
	DefinitionRef string
	Params        json.RawMessage
	TriggerRef    string
	ParentRunID   string
}
type RunSpawner interface {
	SpawnChild(ctx context.Context, req SpawnChildRequest) (runID string, err error)
}
// Config gains: Spawner RunSpawner

// internal/domain, additive
type ChildPlanItem struct { Index int; Params json.RawMessage }
type ChildRunsPlanned struct { RunID, NodeID, ExecID string; Items []ChildPlanItem }
type ChildRunSpawned struct { RunID, NodeID, ExecID string; Index int; ChildRunID string }
type CmdSpawnChildren struct { RunID, NodeID, ExecID string }
const WaitFailed WaitOutcome = "failed" // node.wait.resolved bumped to schema v2

// internal/registry, additive NodeDef/SpawnDef/JoinDef usage
// (SpawnDef/JoinDef already existed, parsed-only, since L01/L03 — this
// document is what validates and dispatches them)
```

## Files to create

```
internal/engine/spawn.go  spawn_test.go  spawn_internal_test.go

cmd/kairos/spawner.go

internal/registry/spawn_test.go

internal/domain/spawn_test.go

internal/events/schemas/child.runs.planned/v1.json  child.run.spawned/v1.json
internal/events/schemas/node.wait.resolved/v2.json
internal/events/fixtures/child.runs.planned/v1.json  child.run.spawned/v1.json
internal/events/fixtures/node.wait.resolved/v2.json

# modified:
internal/domain/advance.go  cmd.go  event.go  transitions.go
internal/registry/validate.go  defaults.go
internal/engine/engine.go  dispatch.go  reconcile.go  shard.go
internal/events/init.go  registry.go  fixtures_test.go  registry_test.go
internal/tasksource/inbox.go  (unrelated real bug fixed along the way — see Errors and fixes)
```

## Data changes

None beyond L02's schema. Every new event type is run-scoped, living in the existing `events`
table under the affected run's own `stream_id`, exactly like every prior document's additive
events.

## Acceptance criteria

- `go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run` clean; `go test ./... -race`
  green across every package, including three repeated `-count` runs of the spawn-specific tests
  with no flake.
- All architecture tests pass, including `TestArchitecture_runCreationNotReachableFromActors` —
  `internal/engine` still never imports `internal/tasksource`; a spawned child's own creation goes
  through the injected `RunSpawner` interface, satisfied in `cmd/kairos` (the composition root)
  exactly like `Executor`/`BootIDProvider` already are.
- A real end-to-end fan-out (`TestEngine_spawnJoinFansOutAndWaitsForAllChildren`) resolves
  `forEach` against a real preceding node's real shell-produced output, creates real child Runs
  through the same `tasksource.TriggerRun` path a trigger source uses, and `join: waitAll` only
  resolves once every child independently reaches a terminal state.
- `onChildFailure: fail` hard-fails the coordinator; `onChildFailure: degrade` absorbs a child
  failure (`run.degraded` recorded, run still reaches `RunSucceeded` once the join completes) —
  both proven end to end, not just at the domain-fold level.
- `maxSpawnDepth` is enforced at spawn-dispatch time from the coordinator's own resolved depth
  (parsed from its `TriggerRef`, walking the spawn-chain recursively).
- Kill-mid-spawn: a coordinator whose children finished terminal while no live engine ever watched
  them still resolves correctly once `Reconcile` (then `Start`) runs on the next boot.
- `make cross` builds `CGO_ENABLED=0` for darwin/linux × arm64/amd64.
- No `TODO`, `FIXME`, or commented-out code in the diff.

## Tests

- `internal/domain/spawn_test.go`: `TestAdvance_waitChildRunDispatchesSpawnChildrenAlongsideEnterWait`
  (mirrors `WaitHuman`/`CmdCreateHumanTask`'s exact pairing), `TestAdvance_waitFailedRoutesViaFailureEdge`.
- `internal/registry/spawn_test.go`: well-formed spawn/join publishes with the correct implied
  defaults (`workspace: none`, `wait.on: [{kind: child-run}]`, `onTimeout: park`,
  `onChildFailure: fail`); `actor: spawn` requires a `spawn:` block and vice versa; `strategy` must
  be `bounded(N)`; `join.mode` must be `waitAll`; `onChildFailure` rejects unknown values; `forEach`
  must reference `$.outputs...`; `inheritWorkspace` must be `clone`.
- `internal/engine/spawn_internal_test.go` (white-box): `parseBoundedStrategy`, the
  `formatSpawnTriggerRef`/`parseSpawnTriggerRef` round-trip and its rejection of unrelated
  `TriggerRef` shapes, `resolveChildDefinitionPath`.
- `internal/engine/spawn_test.go`: the real end-to-end fan-out test; `onChildFailure: fail`/
  `degrade` end-to-end (a real failing child, driven by a real `shell` actor exiting 1);
  `TestReconcile_catchesUpAJoinWhoseChildrenFinishedWhileNoEngineWasWatching` (children seeded
  directly as already-terminal runs, proving `Reconcile`'s catch-up pass — not the live
  `handleChildRunFinished` hook — is what notices and resolves the join).
- `internal/tasksource`: the pre-existing inbox-watcher shutdown race this document's own
  `-race` runs surfaced (see Errors and fixes) is covered by the existing `TestInbox_*` suite,
  now run repeatedly (`-count=10`) with no failure.

## Benchmarks

None. Spawn/join dispatch is not on L02's durability-sensitive hot path at a scale that warrants
one yet — a coordinator's join progression is bounded by real child-run creation cost (SQLite
appends), not a tight loop.

## Migration

None from a prior version. `node.wait.resolved`'s schema bump to v2 is additive and backward
compatible: existing v1-shaped historical events remain decodable via the still-registered v1
schema; only new appends validate against v2's widened `Outcome` enum.

## Future work

- A real named-workflow registry, so `spawn.workflow` can reference something other than a sibling
  file of the coordinator's own definition (decision #2).
- `git fetch <child> && git merge FETCH_HEAD` integration, once the corpus specifies when it should
  happen (decision #7) — likely a dedicated `git.merge` effect provider extending L12's
  `internal/effect` abstraction.
- `forEach` resolution over an oversized (`OutputRef`-backed, L09) output (decision #4).
- "Waves" — the `paths[]` overlap structural check — once a concrete per-forEach-item schema exists
  to compare (decision #9).
- A richer `Degraded`-resolution flow reachable by something other than the causing join's own
  eventual completion — e.g. a human or policy-driven resolution, distinct from L13's
  `wait: human` machinery (decision #8).
- Per-run repo selection for `WorkspaceRepo`/`MirrorRoot` (an L06/L08/L11/L12 gap this document
  inherits unchanged) would let a coordinator's children clone from a repo other than the
  daemon-wide default.
