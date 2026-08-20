# L11 — Policy + secrets

## Depends on

L03 (definition + validator), transitively L02/L01. Also absorbs the "constraints slice 2" work
L10 explicitly deferred here (`L10-constraints-gates.md`'s Documented decisions #1 and Future work):
the remaining five gate kinds, the constitution resolution/merge system, and waivers. Built against
L00-L10, all committed and green.

## Scope

**In.**
- `internal/policy`: `~/.kairos/policy.yaml` effect-permission tiers (allow/confirm/deny),
  wildcard-prefix rule matching, `Default()` matching 05-gates.md's own shipped example.
- `internal/registry/constitution.go`: `BaselineGates` (`kairos/baseline`, compiled in),
  `MergeConstitution` (baseline -> repo -> project, later wins), `LoadWithConstitution` (merges the
  constitution's gate library into a `Definition`'s own `Gates` map, alongside the workflow's inline
  `gates:` block).
- `internal/constraint`: five new gate kinds — `file` (exists/absent globs), `regex`
  (`over: added-lines` only), `git-diff` (pathsForbidden/mustTouch/maxFiles/maxLines/dirty/staged),
  `coverage` (numeric threshold, typed float comparison), `judged` (quorum refutation with
  evidence-required verdicts). A `Judge` interface, implemented by `internal/engine` to avoid a
  circular import.
- `internal/engine`: `actor_judge.go` (a synchronous judge-actor spawn via the existing file-contract
  machinery), `policy.go` (`checkEffects`, `GrantEffectConfirmation`, `GrantWaiver`, `waiverActive`),
  waiver-aware gate-failure routing in `gates.go`, repo-level constitution merge once a workspace
  directory is known.
- `internal/domain`: three additive events (`WaiverGranted`, `EffectConfirmationRequested`,
  `EffectConfirmed`), a new `FailPolicyDenied` `FailReason`.
- `internal/registry/validate.go`: publish-time rejection of a `judged` gate whose `quorum.from`
  names the gated node's own actor ("the judge is never the session under judgement").
- Four new `11-limitations.md` entries (NL-34 through NL-36, plus an update to NL-33).

**Out.** Non-code domain gate kinds (`grounded`/`recipients`/`outbound-scan` — `13-domains.md`, a
later document); mandatory, non-removable gate auto-attachment to every node (05-gates.md's
stage-keyed `mandatoryGates` mechanism — see Documented decision #3); the full pause-the-run-and-
resume-on-a-human-answer confirmation flow (L12/L13 own real effect dispatch and the human queue);
`strategy: fail-fast` constitution-level override (L10's `strategy: all` stays unconditional); CoW
snapshot machinery; effects' actual execution/compensation semantics (L12); a `kairos waiver
grant`/`kairos policy` CLI verb (the engine-level functions are real and tested; the CLI/API surface
wrapping them is a thin, deferred layer).

## Documented decisions

1. **Judged-gate quorum invokes one configured LLM binary for every named judge, not literally
   different CLIs.** `internal/engine.Judge` has only `Config.LLMBinary` (L08's single-binary knob)
   to spawn — a `quorum.from: [reviewer-security, reviewer-security-codex]` becomes two prompts on
   the same binary, not two different tools. Real per-actor binary resolution does not exist
   anywhere in the registry or engine config yet. Registered as NL-36.
2. **The judge invocation is synchronous, spawned directly via `internal/executor/local`, not routed
   through `dispatchLLMActor`'s `CmdStartNode`/admission/domain-event machinery.** A judge invocation
   is gate-evaluation plumbing, not a `NodeExecution` in its own right, and never appears in the run's
   event stream as a node — only its outcome (via `ConstraintEvaluated`) does.
3. **No mandatory, non-removable gate auto-attachment.** `05-gates.md`'s `mandatoryGates` clause
   would merge `guardrails-untouched`/`no-secrets` into every workflow's stage schedule,
   unconditionally. Prototyping this by auto-appending those IDs to every node's `Gates []string`
   broke essentially the entire pre-existing test suite: `no-secrets` is a `regex` kind that requires
   a `BaseRef`, and most nodes across L05-L10 have neither a git workspace nor a configured
   `BaseRef` — forcing the gate onto them fails every run in the system for reasons unrelated to the
   workflow author's intent. `BaselineGates` are instead merely name-resolvable: a workflow author
   who explicitly lists `guardrails-untouched` in a node's own `Gates` gets the real baseline
   definition, but nothing is forced onto a node that did not ask for it. Real mandatory attachment,
   gated on a project actually having a git workspace and `BaseRef` configured, is Future work.
4. **Constitution resolution happens at two different times for two different layers.**
   `loadDefinition` (used by both `dispatchStartNode` and `dispatchEvaluateGates`) merges baseline +
   the project layer on every call — neither needs a workspace to exist. The repo-level layer
   (`<repo>/.kairos/constitution.yaml`, 05-gates.md's "loaded and content-hashed before the run
   starts and never re-read") can only be resolved once `evaluateGates` has provisioned or located a
   real workspace directory, so it is merged locally within that one call, not written back into a
   shared `Definition`. Re-deriving from disk on every gate evaluation, rather than caching a hash
   once at run start, is the "never re-read" property expressed as "always re-read the same
   immutable-by-convention file," not literally cached-and-compared — the actual mid-run tamper
   detection this implies (comparing a cached hash against a fresh read) is not wired into a
   recorded, run-scoped fact; only the hashing mechanism itself (`LoadConstitutionGates` returning
   raw bytes) is real and tested. See Future work.
5. **Confirm-tier effects are a synchronous, this-attempt-only check, not 05-gates.md's full
   pause-and-resume flow.** `checkEffects` requires an `EffectConfirmed` fact to already exist before
   a node with a confirm-tier effect dispatches; absent one, the node fails immediately (after
   recording `EffectConfirmationRequested` for audit) rather than parking the run and resuming once a
   human answers. The full flow needs a new "waiting to start" state-machine addition to
   `internal/domain` that materially overlaps L12 (effects + compensation, which owns real effect
   dispatch) and L13 (the human queue) — building it here would mean building L12/L13's core
   machinery ahead of schedule, which AGENTS §7 forbids. Registered as NL-35.
6. **`GrantWaiver`/`GrantEffectConfirmation` have no CLI/API surface yet.** Both are real, tested,
   callable engine methods — `GrantWaiver` categorically rejects any actor other than exactly
   `"human"`, which is the whole enforcement of "waiver.grant is deny-tier for every non-human
   principal" (not a documentation-only claim: there is no code path, human or otherwise, that can
   append `WaiverGranted` except through this one guarded function). Wrapping them in a
   `kairos waiver grant` / `kairos effects confirm` verb (and the daemon API route +
   `apispec.Ops` entry `TestUI_everyCallHasCLICounterpart` requires) is a thin layer deferred to
   avoid expanding this document's already-large scope further.
7. **A waiver's failure-suppression is scoped to (RunID, NodeID, GateID), not ExecID.** A gate
   re-evaluates on every retry attempt (a new `ExecID`); a human granting a waiver in response to one
   attempt's failure should not have to re-grant it for the next attempt against the same gate. The
   underlying `ConstraintEvaluated` fact is still recorded honestly for every attempt regardless —
   the waiver changes only whether a failure blocks routing, never the evidence trail (05-gates.md's
   "fake the result" defence covers the audit trail; only routing changes here).
8. **`git-diff`/`regex`'s `**` glob support is a minimal prefix/suffix subset, not a general glob
   library.** `filepath.Match` does not support `**`; `"docs/**"` (prefix) and `"**/*_test.go"`
   (suffix) — 05-gates.md's own examples — are handled directly. A `**` in the middle of a pattern
   is not supported. Pulling in a third-party glob library for one gate kind's exclude lists is more
   than this document's scope warrants.
9. **`coverage`'s `baseline: git` (compare against the base ref, never decrease) is not built.** Only
   the numeric-threshold half (`expect: { min }`) is implemented, as a typed float comparison per
   05-gates.md's own stated requirement ("threshold gates must compare numbers"). The baseline
   comparison is a second, materially separate feature (fetching and parsing the base ref's own
   coverage run) — Future work.

## Public interfaces

```go
// internal/policy
type Tier string // Allow | Confirm | Deny
type EffectRule struct { Allow, Confirm, Deny, Match string; Paths []string; Reason string }
type Policy struct { Default string; Effects map[string]EffectRule }
func Load(path string) (Policy, error)
func Default() Policy
func (p Policy) Decide(effect string) Decision

// internal/registry
var BaselineGates map[string]GateDef
func MergeConstitution(repoPath, projectPath string) (map[string]GateDef, []byte, error)
func LoadWithConstitution(path, repoPath, projectPath string) (Definition, error)
// GateDef gains: FileExists/FileAbsent, RegexOver/RegexAbsent/RegexExclude,
// GitDiff{PathsForbidden,MustTouch,MaxFiles,MaxLines,NoBinary,Dirty,Staged},
// CoverageThen/CoverageCaptureRegex/CoverageMin, JudgeActors/JudgeQuorumOf/JudgeLens/JudgeFraming

// internal/constraint
type Judge interface { Judge(ctx context.Context, req JudgeRequest) (JudgeVerdict, error) }
func (e *Evaluator) WithJudge(judge Judge) *Evaluator
// Input gains: BaseRef string

// internal/engine
func (e *Engine) GrantWaiver(ctx context.Context, actor, runID, nodeID, gateID, reason string, expiresAt time.Time) error
func (e *Engine) GrantEffectConfirmation(ctx context.Context, runID, nodeID, effect, scope string) error
// Config gains: ConstitutionProjectPath, Policy policy.Policy, BaseRef string

// internal/domain, additive events
type WaiverGranted struct { RunID, NodeID, GateID, Reason string; ExpiresAt time.Time; GrantedBy string }
type EffectConfirmationRequested struct { RunID, NodeID, ExecID, Effect string }
type EffectConfirmed struct { RunID, NodeID, Effect, Scope string }
const FailPolicyDenied FailReason = "policy-denied"
```

## Files to create

```
internal/policy/policy.go  policy_test.go

internal/registry/constitution.go  constitution_test.go

internal/constraint/file.go  gitdiff.go  coverage.go  judged.go  gitkinds_test.go  judged_test.go

internal/engine/actor_judge.go  policy.go  policy_test.go  gates_waiver_test.go

internal/events/schemas/waiver.grant/v1.json  fixtures/waiver.grant/v1.json
internal/events/schemas/effect.confirmation.requested/v1.json  fixtures/effect.confirmation.requested/v1.json
internal/events/schemas/effect.confirmed/v1.json  fixtures/effect.confirmed/v1.json

# modified:
internal/domain/event.go  advance.go  nodeexecution.go
internal/events/init.go  registry.go  registry_test.go  fixtures_test.go
internal/registry/definition.go  gates.go  validate.go
internal/engine/dispatch.go  engine.go  gates.go
internal/config/config.go
cmd/kairos/serve.go
internal/constraint/constraint.go
11-limitations.md
```

## Data changes

None beyond L02's schema. `WaiverGranted`/`EffectConfirmationRequested`/`EffectConfirmed` are
ordinary run-scoped events in the existing `events` table.

## Acceptance criteria

- `go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run` clean; `go test ./... -race`
  green across every package, with no regression to any L00-L10 test.
- All nine architecture tests pass; `make cross` builds `CGO_ENABLED=0` for darwin/linux ×
  arm64/amd64.
- Constitution merge: baseline + repo + project layers combine correctly, override precedence
  tested at each layer, hash-pinning's underlying mechanism (raw-byte comparison across a file edit)
  is tested directly.
- Waivers: a human-authored `WaiverGranted` unblocks a `waivable: true` gate's failure while its
  `ConstraintEvaluated` fact still records the real, honest outcome; a non-"human" actor is rejected
  by `GrantWaiver` itself; a `waivable: false` gate's failure is never waivable under any
  circumstance (unchanged from L10, still enforced).
- Every new gate kind (`file`/`regex`/`git-diff`/`coverage`/`judged`) has a real pass case and a real
  fail case, exercised against actual git repos, real subprocesses, or a scripted fake judge.
- Policy tiers, exercised through the real engine end to end: `allow` proceeds without friction,
  `deny` fails the node with `FailPolicyDenied` and a mandatory reason, `confirm` blocks without a
  recorded `EffectConfirmed` and proceeds once one exists.
- No `TODO`, `FIXME`, or commented-out code in the diff.

## Tests

- `internal/policy/policy_test.go`: tier resolution, wildcard-vs-exact precedence, `Load`
  missing-file/real-file behavior.
- `internal/registry/constitution_test.go`: baseline-always-present, repo-merged-in,
  project-authoritative-over-repo, workflow-inline-wins-over-every-layer, hash-pinning's raw-byte
  detection, missing-file-is-not-an-error.
- `internal/constraint/gitkinds_test.go`: `git-diff` (clean-tree pass/fail, pathsForbidden,
  maxFiles, BaseRef-required), `regex` (added-lines TODO catch, exclude patterns, clean pass),
  `file` (exists/absent).
- `internal/constraint/judged_test.go`: quorum agreement passes, a single evidenced refutation
  fails, a no-evidence verdict is inconclusive (does not count toward quorum), no configured judge
  fails loudly.
- `internal/engine/gates_waiver_test.go`: the full waivable-true-can-be-waived flow end to end
  against the real engine; `GrantWaiver` rejects a non-human actor and an empty reason.
- `internal/engine/policy_test.go`: allow/deny/confirm tiers end to end against the real engine,
  `EffectConfirmationRequested` audit recording, confirm-then-proceed via
  `GrantEffectConfirmation`.

## Benchmarks

None. Nothing introduced here is on L02's durability-sensitive hot path.

## Migration

None from a prior version.

## Future work

- Real per-actor CLI resolution for judged-gate quorum (NL-36) — today, one binary answers for every
  named judge.
- The full pause-the-run-and-resume-on-a-human-answer confirmation flow (NL-35), once L12 (effects +
  compensation) and L13 (the human queue) exist to own it.
- `~/.kairos/policy.yaml`'s `match`/`paths` sub-pattern scoping (NL-34), once L12 builds the real
  builtin effect call sites whose arguments would feed it.
- Mandatory, non-removable gate auto-attachment (`05-gates.md`'s stage-keyed `mandatoryGates`
  mechanism), gated on a project actually having a git workspace and `BaseRef` configured.
- `strategy: fail-fast` as a constitution-level override of L10's unconditional `strategy: all`.
- A `kairos waiver grant` / `kairos effects confirm` CLI verb and matching daemon API route,
  wrapping the already-real `GrantWaiver`/`GrantEffectConfirmation` engine methods.
- `coverage`'s `baseline: git` comparison against the base ref.
- A recorded, run-scoped fact for the repo-level constitution's content hash, so a genuine mid-run
  tamper attempt is caught by the running engine itself, not just detectable by a fresh caller
  re-reading the file (decision #4).
