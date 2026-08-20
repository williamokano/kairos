# L08 — Actor SDK: rule/shell/llm, file contract, output ladder

## Depends on

L06 (local executor + workspaces), transitively L05/L04/L03/L02/L01. `07-runners.md` and
ADR 0009 (remote SSH runners) are an unrelated, much later, differently-numbered phase — not a
dependency of this document, and not touched by it.

## Scope

**In.**
- `internal/engine`: a new `dispatchLLMActor`, handling actor kinds `claude`/`codex`/`gemini`/
  `local` — 04-agents.md's "an LLM CLI" — as a single configured CLI binary
  (`engine.Config.LLMBinary`), invoked through the file contract (`KAIROS_OUTPUT`/`KAIROS_SCHEMA`
  env vars, prompt on stdin, never argv). Reads only the CLI's final `output.json`; no
  `stream-json` parsing (NL-28).
- `internal/executor/local`: `ExecSpec.Stdin []byte`, wired into `Start` — the prompt-on-stdin
  requirement 04-agents.md calls out as one of "five details that matter" (argv is visible in `ps`
  to every process on the machine).
- `retry.mutate` resolution moved into `internal/engine/dispatch.go`'s `dispatchStartNode`: the
  actor a given `CmdStartNode.Attempt` actually invokes is resolved against `nd.Retry.Mutate`
  before the actor-kind switch — 04-agents.md's escalation ladder ("attempt 2 on a stronger model,
  attempt 3 on a different CLI"), reusing the retry/attempt machinery `internal/registry` and
  `internal/domain` already had rather than building a second one.
- 04-agents.md's Stage 2 in-session repair turn: one bounded extra CLI invocation, in the same
  session/workDir, when the first output fails schema validation — `output.repair.attempted` is
  recorded either way.
- Four additive, run-scoped `internal/domain` events (`LLMSessionStarted`, `SessionResumeFailed`,
  `SessionCostUnavailable`, `OutputRepairAttempted`) — unlike L05's system-stream additions, these
  carry a `RunID` and ARE folded through `domain.Advance`, as explicit no-op audit transitions.
- Kairos Sessions: a session identity (a fresh ULID) minted per top-level attempt, with the
  `SessionAffinity: node`/`run` resume attempt gated on 04-agents.md's own named failure mode — the
  path-keying trap (the CLI's session store is keyed by cwd; if the recorded session's `Dir` isn't
  this attempt's `workDir`, resuming would silently find nothing) — detected and recorded via
  `session.resume.failed` before ever trying to resume, never attempted and left to fail silently.
- `registry.NodeDef.OutputSchemaRaw` — the same schema `OutputSchema` compiles, kept as raw bytes,
  since a `*jsonschema.Schema` doesn't marshal back to its source document and both
  `$KAIROS_SCHEMA` and `kairos check-output` need a file.
- `registry.ValidateFile(outputPath, schemaPath) (bool, []string, error)` — the single validator
  both `kairos check-output` and the llm actor's own repair-turn logic call, producing
  `<json-pointer>: <message>` violation lines capped at 20.
- `kairos check-output`: a new CLI verb, local-only (no daemon round trip, no `apispec.Op` — added
  to `internal/archtest`'s `exemptCLIVerbs`), reading `$KAIROS_OUTPUT`/`$KAIROS_SCHEMA` from the
  env the engine already set.
- `internal/workspace.Manager.applyCredentialGuard`: 04-agents.md's "capability, not permission"
  design, applied to every `Provision`-d workspace (not llm-specific — a workspace-safety property):
  blocked `origin` pushurl, disabled credential helper/hooks/askpass.
- `internal/config.Config` gains `WorkspaceRepo`/`LLMBinary`, actually wired into
  `cmd/kairos/serve.go`'s `engine.New` call — L06 added `engine.Config.WorkspaceRepo` but never
  connected it to a real config source; this document closes that gap since it needed the
  identical wiring for `LLMBinary` anyway.
- `11-limitations.md`: NL-28, NL-29, NL-30 (stream-json absence, single-binary/no-sandboxing,
  cost always unknown).

**Out.** Real per-CLI flag probing/shapes (`--session-id`, `--output-format stream-json`,
`--permission-mode`, Codex's `--sandbox workspace-write`) — NL-29; `stream-json` incremental
parsing, pre-emptive cost enforcement, turn-idle timeout, compaction detection/re-injection —
NL-28; Stage 3 extraction (constrained decoding via a local model) — needs a real local-model
call this document does not add; a real timer wheel / node-level timeout enforcement (L05's
decision #6, still open); artifact collection/redaction beyond what NL-18 already covers; real
gate evaluation (L10); effects/compensation (L12); remote runners (unrelated phase, see Depends
on).

## Documented decisions

1. **A single configured `LLMBinary`, not per-actor-kind binaries or flag probing.** 04-agents.md
   describes three materially different CLIs (Claude Code, Codex, local/Ollama) with different
   flags, sandboxing, and even transport (Ollama is an HTTP call, not a subprocess at all). Building
   all three, plus the `<cli> --version`/`--help` probing 04-agents.md says every flag needs, is
   scope this document does not carry — and this environment has none of those CLIs installed to
   test against regardless. `claude`/`codex`/`gemini`/`local` all resolve to the identical
   invocation shape against one configured binary. Registered as NL-29, not silently narrowed.
2. **`retry.mutate` resolution lives in `dispatch.go`, not `domain`.** Confirmed by reading
   `domain.Advance`/`dispatchExec`: `CmdStartNode` carries only `NodeID`/`Attempt`/`Iteration`, no
   actor field, and nothing in `internal/domain` reads `RetryDef.Mutate` at all — it was already a
   pure engine-level dispatch-time concern before this document, just unexercised until now. No new
   retry system; the existing `Attempt`-bounded ladder just gets an actor lookup added at the one
   place it's dispatched.
3. **Stage 2 (in-session repair) and Stage 4 (a fresh top-level attempt) are genuinely different
   mechanisms, not the same ladder run twice.** Stage 2 is one extra subprocess spawn inside the
   SAME `NodeExecution`/`ExecID` — no new domain event drives it, no `Attempt` increment, just an
   `output.repair.attempted` fact and a second `checkOutput` call before the ordinary
   `NodeOutputReceived` is appended. Stage 4 is domain's pre-existing `SchemaValid: false` →
   `handleFailureOutcome` → next-attempt path, reached simply by reporting the repaired-or-not
   truth honestly. Conflating them would mean either a repair turn silently consuming a whole
   retry attempt (wasteful — 04-agents.md is explicit repair is "strictly better...costs one
   process spawn") or domain gaining a repair concept it has no business knowing about.
4. **Kairos Session identity is a ULID minted per top-level attempt**, not something `domain`
   tracks (no `NodeExecution.SessionID` field — sessions are an engine/actor-invocation concern,
   same posture as `RestartPolicy` in L05/L06). `LLMSessionStarted.Dir` is recorded specifically so
   a LATER attempt can detect the path-keying trap without a live process to ask.
5. **The path-keying trap fires for every non-`workspace: write` retry, not just a contrived
   case.** A node's scratch dir is `workRoot/runID/execID` — per-exec, hence different on every
   attempt — so `resolveSession` finds `prior.Dir != workDir` and records `session.resume.failed`
   on essentially every multi-attempt llm-actor node that isn't `workspace: write` (whose dir is
   stable at `workRoot/runID/repo` across attempts, from L06). This is the correct, honest
   behaviour 04-agents.md's own mitigation describes ("keep the workspace path a function of
   (runID, repo) so it is stable across attempts") — not a bug in this document's test, which is
   why `TestEngine_llmActorSessionResumeFailsAcrossAttemptsWithoutAWorkspace` needs no extra
   plumbing to exercise it.
6. **Cost is always `unknown`.** NL-28 (no stream parsing) makes 04-agents.md's tiers 1 and 2
   (CLI-reported total, price-table computation) both unreachable; `session.cost.unavailable` is
   emitted after every llm-actor execution, honestly, per AGENTS §4 rule 1 rather than fabricating
   a number. Registered as NL-30.
7. **The credential guard applies to every `Provision`-d workspace, not just llm actors.** It is a
   workspace-safety property (a real remote origin is never wired to any process running inside
   the clone), not an actor-kind concern — a `shell` actor with `workspace: write` gets the
   identical guard `internal/workspace.Manager.Provision` now applies unconditionally.
8. **`kairos check-output` needs no daemon connection.** It validates a local file against a local
   file — an actor invokes it from deep inside its own subprocess, where a daemon round trip would
   be one more thing that can fail for no reason. Exempted from `TestUI_everyCallHasCLICounterpart`
   accordingly (it has no `apispec.Op` to map to, by design, not by omission).
9. **`WorkspaceRepo`/`LLMBinary` are `internal/config` fields, wired at `kairos serve` boot** —
   closing a real gap L06 left open (it added `engine.Config.WorkspaceRepo` but never connected it
   to any config source, so no real daemon boot could ever set it). `internal/config`'s own doc
   comment says its schema "is added incrementally by the documents that own each subsystem" —
   this document owns `LLMBinary` and needed the identical env-var wiring pattern for
   `WorkspaceRepo` anyway.

## Public interfaces

```go
// internal/executor/local, ExecSpec addition
type ExecSpec struct {
	// ... existing fields ...
	Stdin []byte // written to the child's stdin and closed; nil means none
}

// internal/engine.Config addition
LLMBinary string // configured CLI binary for claude/codex/gemini/local actor kinds

// internal/registry, additive NodeDef field
OutputSchemaRaw json.RawMessage // OutputSchema's source document, undecoded

// internal/registry
func ValidateFile(outputPath, schemaPath string) (bool, []string, error)

// internal/domain, additive run-scoped events (folded by Advance as no-ops)
type LLMSessionStarted struct{ RunID, NodeID, ExecID, SessionID string; Resumed bool; Dir string }
type SessionResumeFailed struct{ RunID, NodeID, ExecID, PriorSessionID string }
type SessionCostUnavailable struct{ RunID, NodeID, ExecID string }
type OutputRepairAttempted struct{ RunID, NodeID, ExecID string; Errors []string }

// internal/workspace addition
func (m *Manager) applyCredentialGuard(ctx context.Context, dir string) error // unexported, called by Provision

// internal/config, additive Config fields
WorkspaceRepo, LLMBinary string

// internal/cli
func newCheckOutputCmd() *cobra.Command // "kairos check-output"
```

## Files to create

```
internal/engine/actor_llm.go  actor_llm_test.go

internal/cli/checkoutput.go  checkoutput_test.go

internal/registry/schema_test.go  (extended: compileSchemaDocWithRaw, ValidateFile cases)

internal/workspace/manager_test.go  (extended: TestManager_provisionAppliesTheCredentialGuard)

# modified:
internal/domain/event.go  advance.go
internal/events/schemas/{llm.session.started,session.resume.failed,session.cost.unavailable,output.repair.attempted}/v1.json
internal/events/fixtures/{llm.session.started,session.resume.failed,session.cost.unavailable,output.repair.attempted}/v1.json
internal/events/init.go  registry.go  fixtures_test.go  registry_test.go
internal/registry/definition.go  defaults.go  schema.go
internal/executor/local/spec.go  spawn.go
internal/engine/engine.go  dispatch.go
internal/workspace/manager.go
internal/config/config.go
internal/cli/root.go
internal/archtest/ui_cli_parity_test.go
cmd/kairos/serve.go
11-limitations.md
```

## Data changes

None beyond L02's schema. The four new event types are ordinary rows in the existing `events`
table, on each event's own run stream (not the system stream) — the first documents' additive
events to actually be folded through `domain.Advance` rather than skipped.

## Acceptance criteria

- `go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run` clean; `go test ./... -race`
  green across every package, including `cmd/kairos`'s full-binary integration suite.
- All nine architecture tests pass, including `TestUI_everyCallHasCLICounterpart` with
  `check-output` in `exemptCLIVerbs`.
- `TestEngine_llmActorRunsFakeCLIToSuccess`: a real subprocess (a fake CLI script, never a real
  network call) driven through the full file contract to a successful run.
- `TestEngine_llmActorRepairsInvalidOutputThenSucceeds`: exactly one `output.repair.attempted` and
  exactly one `node.execution.started` — the repair turn is proven NOT to be a second top-level
  attempt.
- `TestEngine_llmActorSessionResumeFailsAcrossAttemptsWithoutAWorkspace`: two
  `llm.session.started` (both `Resumed: false`) and exactly one `session.resume.failed` across two
  failing attempts.
- `TestManager_provisionAppliesTheCredentialGuard`: `origin`'s pushurl, credential helper, hooks
  path, and askpass are all verified via a real `git config --get` round trip against the
  provisioned clone.
- `kairos check-output` unit-tested against valid/invalid/missing-env fixtures with no daemon
  running.
- `make cross` builds `CGO_ENABLED=0` for darwin/linux × arm64/amd64.
- No `TODO`, `FIXME`, or commented-out code in the diff.

## Tests

- `internal/engine/actor_llm_test.go`: the three tests named in Acceptance criteria, plus the
  llm-actor happy path's assertion that `llm.session.started` is recorded.
- `internal/registry/schema_test.go`: `compileSchemaDocWithRaw`'s raw bytes round-trip; `ValidateFile`
  against valid, invalid, and missing-file fixtures.
- `internal/cli/checkoutput_test.go`: valid output (exit 0, silent), invalid output (non-zero,
  violation lines printed), missing env vars (usage error) — all via `cli.RootCommand()`, no
  daemon.
- `internal/workspace/manager_test.go`: the credential-guard test, added alongside L06's existing
  provision/GC tests.
- `internal/domain`, `internal/events`: the four new event types flow through the existing
  fixture-projection and registry-count tests unchanged in shape (added as new cases, not new
  test files).

## Benchmarks

None. Nothing introduced here is on L02's durability-sensitive hot path.

## Migration

None from a prior version.

## Future work

- Real per-CLI command shapes and flag probing (NL-29) — Claude's `--session-id`/`--resume`/
  `--permission-mode`, Codex's `--sandbox workspace-write`, Ollama's constrained-decoding HTTP
  path (Stage 0, "the only path with a guarantee" per 04-agents.md — currently entirely unbuilt).
- `stream-json` parsing (NL-28) and everything downstream of it: pre-emptive cost enforcement,
  turn-idle timeout, compaction detection/re-injection, coalesced progress events.
- Stage 3 extraction (constrained-decoding recovery when a runner can't resume) — needs Stage 0's
  local-model path to exist first.
- A real, persisted timer wheel (L05's decision #6, still open) — the node-level timeout backstop
  04-agents.md lists first has no enforcing code path until one exists.
- Per-run repo selection (L06's decision #1, still open) — `WorkspaceRepo` stays one daemon-wide
  value.
- Real host/owner/repo-keyed mirror deduplication (L06's decision #2, still open).
