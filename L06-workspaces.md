# L06 — Local executor + workspaces

## Depends on

L05 (engine dispatch, replay, reconciliation), transitively L04/L03/L02/L01. This document
completes the two things L05 deliberately deferred: real `workspace: write` clones (decision #7)
and the `adopt` restart policy (decision #12).

## Scope

**In.**
- `internal/workspace`: ADR 0005's `--reference` clone-per-run scheme — a bare, gc-disabled mirror
  per source repo, borrowed from by a private per-run clone via git's object alternates. Every git
  invocation goes through `internal/executor/local`'s `Executor`, never `os/exec` directly
  (AGENTS.md §2: "`internal/workspace` runs `git` through the executor, not through
  `exec.Command`").
- `internal/engine`: `workspace: write` nodes now provision a real clone via `internal/workspace`
  instead of L05's bare scratch directory placeholder; nodes that don't declare `workspace: write`
  are unaffected.
- The `adopt` `RestartPolicy`, implemented in `internal/engine/reconcile.go`'s `reconcileRun`: an
  alive orphan whose node resolves to `adopt` is left running and watched for its real, natural
  exit instead of being killed.
- `internal/registry/validate.go`'s L05-era unconditional rejection of `restartPolicy: adopt` is
  removed now that the machinery it was waiting on exists.
- A tested, unwired `internal/workspace.Manager.GC` for reclaiming inactive runs' workspace
  directories.

**Out.** CoW snapshot probing (`clonefile`/btrfs/reflink — full snapshot machinery is a later
document, not needed for this one's adopt/workspace milestone); the cookie-sweep-for-
double-forked-escapees stress-test acceptance bar `06-durability.md` names (this document's scope
doesn't require it); `llm`/`claude` actor invocation (L08); real gate evaluation (L10);
artifacts/effects (L09/L12); per-run repo selection (see decision #1).

## Documented decisions

1. **One daemon-wide `WorkspaceRepo`, not a per-run repo.** `03-workflows.md` says `workspace`
   defaults to "read, sourced from the repo containing cwd" — i.e. the repo a run's *trigger* was
   invoked from. Nothing in the codebase captures that cwd yet: no field on `TriggerReceived`, no
   CLI flag, no `POST /runs` request field. Threading it through the whole trigger→API→domain-event
   path is real, cross-cutting scope this document does not add. Instead, `engine.Config` gains a
   single `WorkspaceRepo string` — set once at `kairos serve` boot — that every `workspace: write`
   node in every run clones from. Per-run repo selection, sourced from the trigger's invocation
   cwd, is Future work, naturally owned by whichever document first threads `TriggerReceived`
   through a CLI-captured field (L16 triggers or L17 child runs are the likely home).
2. **The mirror key is derived purely from the source repo's absolute path** (a short SHA-1 prefix
   plus basename), not from `git remote get-url origin`'s host/owner/repo. No real GitHub-hosted
   trigger flow exists yet to need cross-machine mirror dedup by remote URL, and parsing every
   remote URL shape (`https://`, `git@host:`, bare paths) correctly is its own scope. Real
   host/owner/repo-keyed mirrors are Future work, revisited whenever a document actually needs two
   different local clones of "the same" remote to share one mirror.
3. **Mirror refresh (`git fetch`) on reuse is not implemented.** `ensureMirror` creates a mirror on
   first use and reuses it as-is afterward — correct for this document's single-clone-per-process-
   lifetime test scenarios, incomplete for a long-running daemon watching an actively-changing
   repo. Named as Future work rather than silently assumed away.
4. **ADR 0005's third refusal — "your own checkout with uncommitted changes" — is not enforced.**
   The two path-based refusals (`$HOME`, filesystem root, and any path inside Kairos's own state
   directory) are pure `os.Stat`/string comparisons and are enforced. The uncommitted-changes
   refusal needs a `git status --porcelain` round trip per provision; adding it is Future work, not
   silently dropped (AGENTS §4 rule 1).
5. **`Reprovision` is never called automatically by reconciliation.** `06-durability.md`'s rule —
   "never `rm -rf` a workspace during recovery, a dirty tree may be the only copy of work" — is
   honoured by making `Reprovision` an explicit, caller-invoked operation for the one case
   `06-durability.md` actually names (`workspace.corrupt` → re-provision from scratch), never
   something reconciliation reaches for on its own. Nothing in this document's reconciliation path
   calls it; it exists, is tested, and is available to whichever later document adds
   corruption-detection logic that decides to invoke it.
6. **`internal/workspace.GC` exists, is tested, and is not wired to a timer.** No timer wheel
   exists (matching L05-engine.md's decision #6 precedent: name it as Future work, don't silently
   skip building the function). A caller — a future `kairos db gc` verb, or a scheduled maintenance
   pass once a timer wheel exists — invokes it explicitly.
7. **Adoption re-attaches by identity-checked polling, not `Executor.Wait`.** `local.Local.Wait`
   only works for a pid the *same* `*Local` instance's `Start` produced (it looks the pid up in its
   own in-memory `tracked` map) — after a daemon restart, the fresh `*Local` has an empty map and
   cannot `Wait` on a process it did not spawn. `adoptWatch` instead polls `local.Probe` (the same
   `(bootID, pgid)` identity check reconciliation already uses for every other verdict) every
   500ms until the process is gone, then folds the outcome through the normal
   `NodeOutputReceived`/`NodeExecutionFailed` path — never through `NodeExecutionLost`, since
   nothing was ever lost.
8. **An adopted node's eventual fold counts as `Recovered` in `ReconcileReport`, not a new field.**
   `EngineReconciled`'s schema (`{Recovered, Lost, OrphansReaped}`) predates this document and
   already fits the semantics — "recovered its liveness tracking" — without a schema change to an
   event already checked into `internal/events/schemas`. `report.recovered++` happens synchronously
   when the adoption watcher is launched, at Reconcile time, since the actual fold happens
   asynchronously and Reconcile must return promptly (adoption's whole point is not blocking
   startup on a long-running process).
9. **`adoptWatch` runs on `Reconcile`'s own incoming context, not a derived one.** In production
   (`cmd/kairos/serve.go`) that context is the long-lived `signal.NotifyContext` result, cancelled
   exactly once, on shutdown signal, before `Engine.Stop` runs — so the watcher goroutine reliably
   exits via `ctx.Done()` if a shutdown happens mid-adoption. This assumes `Reconcile` is never
   called with a short, Reconcile-call-scoped deadline context in production; it is documented here
   rather than defended with a second context parameter this document's scope doesn't need.

## Public interfaces

```go
// internal/workspace
type Manager struct{ /* unexported */ }
func New(mirrorRoot, workRoot string, exec local.Executor) *Manager
type Workspace struct{ RunID, Dir string }
func (m *Manager) Provision(ctx context.Context, runID, sourceRepo string) (Workspace, error)
func (m *Manager) Reprovision(ctx context.Context, runID, sourceRepo string) (Workspace, error)
func Verify(dir string) bool
func (m *Manager) GC(ctx context.Context, activeRunIDs map[string]bool) ([]string, error)

// internal/engine, additive Config fields
MirrorRoot    string // defaults to WorkRoot's parent + "mirrors"
WorkspaceRepo string // empty => workspace: write nodes fail loudly, not silently

// internal/registry
// RestartAdopt now validates successfully at publish time.
```

## Files to create

```
internal/workspace/manager.go  manager_test.go

cmd/kairos/adopt_test.go
cmd/kairos/testdata/adopt.yaml

# modified:
internal/engine/engine.go  actor_shell.go  reconcile.go
internal/registry/validate.go  definition.go
internal/registry/restartpolicy_test.go
```

## Data changes

None. Workspaces live at `$KAIROS_HOME/work/<runID>/repo`, mirrors at `$KAIROS_HOME/mirrors/...` —
both plain directories, not database rows.

## Acceptance criteria

- `go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run` clean; `go test ./... -race`
  green across every package, including the new `internal/workspace`.
- `internal/workspace`'s tests prove: a provisioned clone shares objects with its mirror (a
  non-empty `.git/objects/info/alternates`) and contains the mirror's committed content;
  `Provision` is idempotent (a second call reuses the existing workspace rather than re-cloning
  over it); `Reprovision` replaces a workspace whose `.git` was removed; `GC` removes only inactive
  runIDs' directories; a source path inside Kairos's own state directory is refused.
- `internal/registry`: `restartPolicy: adopt` now validates successfully
  (`TestLoad_acceptsRestartPolicyAdopt`, replacing L05's rejection test).
- `internal/engine`: `TestReconcile_adoptedAliveProcessIsNotKilledAndItsExitIsFolded` proves an
  alive orphan under `adopt` is never signalled, records no `node.execution.lost`, and its real,
  natural exit is what produces `node.output.received`.
- `cmd/kairos/adopt_test.go`'s `TestEngine_adoptSurvivesRestartWithoutKillingTheChild` proves the
  same claim against the real built binary: SIGKILL the daemon mid-node on an `adopt`-policy node,
  restart, and confirm the *same* pid is still alive and untouched immediately after
  `engine.reconciled`, exactly one `node.execution.started` exists for the node (no retry
  dispatched), and the run reaches `run.succeeded` from that one process's natural completion, with
  `kairos db verify` clean throughout.
- `make cross` builds `CGO_ENABLED=0` for darwin/linux × arm64/amd64.
- `make arch` (all nine architecture tests) passes, confirming `internal/workspace` was not added
  to `noExecOutsideExecutor`'s exemption list.
- No `TODO`, `FIXME`, or commented-out code in the diff.

## Tests

- `internal/workspace/manager_test.go`: reference-clone-shares-objects, idempotent provisioning,
  corrupt-workspace reprovisioning, GC's active/inactive partition, the state-directory source
  refusal — all against real git repositories and a real `local.Local` executor, no fakes (the
  claims are about real git/filesystem behavior).
- `internal/registry/restartpolicy_test.go`: `adopt` now parses and validates (was: rejected).
- `internal/engine/reconcile_test.go`: `TestReconcile_adoptedAliveProcessIsNotKilledAndItsExitIsFolded`
  — a real short-lived subprocess, reaped by a background goroutine to model reparent-to-init
  (kill(pgid,0) reports a zombie as alive until reaped), proving adoption's non-kill and eventual-
  fold behavior.
- `cmd/kairos/adopt_test.go`: the real-binary end-to-end adoption test described above.

## Benchmarks

None. Workspace provisioning is a per-run, once-per-node-lifetime operation (roughly one `git
clone --reference` per run, ADR 0005 estimates ~1 second for a 200MB repo) — not a hot path this
document's scope puts under load.

## Migration

None from a prior version.

## Future work

- Per-run repo selection sourced from the trigger's invocation cwd (decision #1) — the natural
  extension once `TriggerReceived` or `POST /runs` needs to carry it (L16/L17).
- Host/owner/repo-keyed mirrors parsed from `git remote get-url origin`, so two local clones of the
  same remote share one mirror (decision #2).
- Mirror refresh (`git fetch`) on reuse, for a long-running daemon watching an actively-changing
  repo (decision #3).
- ADR 0005's uncommitted-changes-in-your-own-checkout refusal, via `git status --porcelain`
  (decision #4).
- Corruption *detection* that decides when to call the already-built `Reprovision` (decision #5) —
  this document builds the mechanism, not the detector.
- Wiring `internal/workspace.GC` to a real maintenance pass, once a timer wheel or a `kairos db gc`
  verb exists (decision #6).
- CoW snapshot probing (`clonefile`/btrfs/reflink) for `freshWorkspace: true` retries at a scale
  where a fresh `--reference` clone's ~1 second stops being negligible.
- The cookie-sweep-for-double-forked-escapees stress test `06-durability.md` names as an
  acceptance bar ("no orphaned processes after 50 runs, including 10 SIGKILLed mid-flight") —
  this document's two flagship adoption/kill tests prove the mechanism; the stress-scale version is
  a later hardening pass.
