# L14 — Conversations

## Depends on

L04 (daemon/API/CLI) only. Despite the number, L14 is a genuinely early document on the build
plan's dependency graph (`12-build-plan.md`: `L04 --> L14 --> L13 --> L12`) — it is what unblocks
L13 (human decisions), which in turn unblocks L12 (effects + compensation). Built against L00-L11,
all committed and green; no dedicated `L14-*.md` source doc existed before this one — its scope is
assembled from three scattered sections: `04-agents.md`'s "Sessions: two concepts, never
conflated" (native resume), `03-workflows.md`'s `wait: conversation` mention, and
`09-cli-and-tui.md`'s Conversation screen mockup.

## Scope

**In.**
- `internal/conversation`: `AppendMessage`/`Messages` — the read/append primitive for a
  Conversation's own event stream, shared by `internal/api` and `internal/engine` without either
  depending on the other.
- `internal/eventstore`: `ConversationStreamPrefix`, `ConversationStreamID`,
  `RunIDFromConversationStream`, and `IsAuxStream` (generalising L05's `SystemStream`-only skip
  check in `RunStateProjection`, `RunIndexProjection`, `Verify`/`Rebuild`, and `Engine.Start`'s
  subscribe loop to cover any aux-stream namespace, not just `system`).
- `internal/domain`: one additive event, `ConversationMessageAppended` (never folded — same
  non-run-scoped posture as L05's system-stream events); `ResumeMode` added to L08's
  `LLMSessionStarted` (additive field, same schema version — no `additionalProperties: false`
  anywhere in this codebase's schemas, so a new optional property is a safe within-version
  addition, not a new version).
- `internal/engine`: `wait: conversation` made real — `resolveConversationWait`
  (`conversation.go`) resolves an `ExecWaiting` node whose `WaitSpec.Kind == WaitConversation`
  against the Conversation's latest message, both live (the central `Start` loop, on every new
  `conversation.message.appended`) and during `Reconcile` (catching a message that arrived while
  the daemon was down — `Store.Subscribe` only delivers events appended after subscribing).
  `actor_llm.go`'s `nativeResumeArgv` makes 04-agents.md's "native" resume mode a genuine part of
  the CLI invocation (`--resume <id>` / `exec resume <id>`), not just the `KAIROS_RESUME_SESSION_ID`
  env var L08 already recorded.
- `internal/api` + `internal/apispec` + `internal/cli`: `GET /runs/{id}/conversation` and
  `POST /runs/{id}/conversation/messages`, with `kairos conversation show`/`kairos conversation
  send` CLI counterparts (`TestUI_everyCallHasCLICounterpart` parity maintained).

**Out** (named explicitly, not silently skipped) — see Future work.

## Documented decisions

1. **Conversation is scoped 1:1 with Run, not the project/task-level, multi-run thread
   `09-cli-and-tui.md`'s mockup shows** ("3 runs · $2.90 · workspace clean"). Building the
   aggregate above Run that groups several runs under one composer-driven thread needs a concept
   this codebase does not have yet (a "task" or "project session"), and is deferred — see Future
   work. `stream_id = "conversation:" + runID` is a direct, derivable key requiring no new
   bookkeeping table.
2. **The append-only exception `09-cli-and-tui.md`:444 describes ("conversation-message append as
   the single exception" to invalidate-and-refetch) is a TUI rendering rule, not a data-durability
   exception.** Read in full, it is about how a not-yet-built renderer updates its own view
   incrementally rather than refetching — nothing about the event store's actual append-only
   invariant is weakened anywhere in this document. No architecture test needed changing.
3. **`wait: conversation` resolves against the Conversation's single latest message**, not a
   cursor tracking "the message after the wait began." Any new message on the stream is treated as
   capable of resolving a currently-waiting exec; `domain.legalExecEvents`' one-`NodeWaitResolved`-
   per-`ExecWaiting`-row invariant makes a second, redundant resolution attempt a harmless
   `ErrIllegalTransition` no-op rather than a double-fire. Simpler and correct for this document's
   single-node-waits-at-a-time scope; a true position cursor is Future work if a workflow ever
   needs "resolve only on message N, not any message."
4. **`resolveConversationWait` takes a `dispatchCmds bool`, distinguishing the live caller from the
   Reconcile caller.** Live: the `NodeWaitResolved` this appends lands on the run's own stream,
   which the live `Subscribe` loop is already watching, so the owning shard dispatches the
   resulting `CmdEvaluateGates` itself — dispatching it here too would run it twice. Reconcile:
   no shard exists yet (this runs before `Start`), so nothing else will ever act on the resulting
   command unless this call does — the same pattern `recoverLost` already established for a Lost
   node's retry dispatch.
5. **Native resume argv is best-effort per actor kind, not a full per-CLI grammar model.**
   `claude` gets `--resume <id>`; `codex` gets `exec resume <id>`; `gemini`/`local` get nothing
   (04-agents.md: `gemini` has no documented native resume, `local` is this repo's own placeholder
   kind). This does not model whether a *fresh* invocation of a given CLI needs its own subcommand
   prefix (a real `codex` invocation likely needs `exec <prompt>` even on attempt 1) — L08 already
   established that the whole actor invocation is a minimal, CLI-agnostic shape (`Argv: []string{
   llmBinary}`, prompt on stdin, no per-CLI flag probing); this document extends that same minimal
   posture to the resume case rather than building the real per-CLI grammar L08 explicitly deferred.
6. **`ConversationMessageAppended.Role` is always `"human"` in this document's scope.** The field
   exists for forward compatibility with an `"actor"`/`"system"` role a later document may add
   (matching the Conversation screen's "implementer ▸run_01A8x" run-summary cards), but nothing
   here ever writes one — those cards are a future TUI's own projection over run state plus
   Conversation messages, not a new kind of message this document produces.
7. **A real bug, found by this document's tests, fixed at the root: `Engine.Stop`'s already-fixed
   context handling (L05) had a sibling gap in `internal/engine`'s admission drain path.**
   `dispatchStartNode`'s admission decision (`TryAdmit`, then `enqueuePending` if `Queued`) was not
   synchronized against a concurrent `releaseAndDrain` — a genuine retry-onto-the-same-workspace
   scenario (this document's own native-resume test, the first `internal/engine` test to combine
   `workspace: write` with a real retry) could observe a `Queued` verdict, then lose the race to
   append itself to the pending list until *after* the release that would have unblocked it had
   already found the list empty, stranding the node forever. Fixed by having `releaseAndDrain` and
   a new `admitOrQueue` share `drainMu` across the decision, not just the drain loop — with
   dispatch (actor spawn) deliberately kept *outside* the lock (`decidePendingLocked` decides,
   `dispatchDrained` runs after unlocking), since a synchronously-failing dispatch calls back into
   `releaseAndDrain` (`dispatchLLMActor`'s own `if !spawned` defer) and re-acquiring an
   already-held `sync.Mutex` from the same goroutine would deadlock.

## Public interfaces

```go
// internal/conversation
func AppendMessage(ctx context.Context, store eventstore.Store, runID, role, text string) error
func Messages(ctx context.Context, store eventstore.Store, runID string) ([]domain.ConversationMessageAppended, error)

// internal/eventstore, additive
const ConversationStreamPrefix = "conversation:"
func ConversationStreamID(runID string) string
func RunIDFromConversationStream(streamID string) (string, bool)
func IsAuxStream(streamID string) bool

// internal/domain, additive
type ConversationMessageAppended struct{ Role, Text string }
// LLMSessionStarted gains: ResumeMode string

// internal/api
GET  /runs/{id}/conversation                -> {"messages": [...]}
POST /runs/{id}/conversation/messages       -> 201, body {"text": "..."}

// internal/cli
kairos conversation show <runID>
kairos conversation send <runID> <text>
```

## Files to create

```
internal/conversation/conversation.go  conversation_test.go

internal/api/conversation.go
internal/cli/conversation.go

internal/engine/conversation.go
internal/engine/wait_conversation_test.go  native_resume_test.go

# modified:
internal/domain/event.go
internal/events/init.go  registry.go  fixtures_test.go  registry_test.go
internal/events/schemas/conversation.message.appended/v1.json  llm.session.started/v1.json
internal/events/fixtures/conversation.message.appended/v1.json
internal/eventstore/store.go  projection_runstate.go  projection_runindex.go  rebuild.go
internal/engine/engine.go  dispatch.go  admission.go  actor_llm.go  reconcile.go
internal/api/server.go
internal/apispec/ops.go
internal/cli/root.go  client.go
```

## Data changes

None beyond L02's schema — the Conversation stream reuses the existing `events` table with
`stream_id = "conversation:<runID>"`, excluded from `run_state_projection`/`run_index` folding and
from `Store.Verify`/`Store.Rebuild` via the generalised `IsAuxStream` check.

## Acceptance criteria

- `go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run` clean; `go test ./... -race`
  green across every package, including `cmd/kairos`'s real-binary flagship tests.
- `TestEngine_waitConversationSuspendsThenResumesOnMessage`: a `wait: conversation` node genuinely
  suspends (does not silently proceed within a real 1-second observation window) and genuinely
  resumes (does not hang) once a message is posted, driven against the live engine loop.
- `TestEngine_waitConversationReconcileCatchesUpOnBacklog`: a message posted while no engine was
  subscribed is not lost — `Reconcile`'s catch-up pass resolves it.
- `TestEngine_llmActorUsesNativeResumeFlagOnSecondAttempt`: a fake CLI scripted to fail unless
  invoked with `--resume` as its first argument only succeeds because attempt 2's argv genuinely
  carries the native resume flag — proving decision #5's mechanism end to end, not just that an
  env var was set.
- `TestUI_everyCallHasCLICounterpart` still passes with the two new conversation routes/verbs.
- `make cross` builds `CGO_ENABLED=0` for darwin/linux × arm64/amd64; `make arch` passes all nine
  architecture tests.
- No `TODO`, `FIXME`, or commented-out code in the diff.

## Tests

- `internal/conversation/conversation_test.go`: append-then-read ordering, an empty conversation
  returns empty not an error, 20 concurrent appends all land and are ordered (proving the CAS-retry
  budget genuinely handles real contention on this human-facing write path — the first version used
  `internal/engine`'s own 5-retry constant and lost 17 of 20 under load; fixed by raising it to 50,
  documented as a deliberate divergence from `appendNext`'s budget, not an oversight), and the
  stream never trips a fold through the run projections.
- `internal/engine/wait_conversation_test.go`: the two tests named in Acceptance criteria.
- `internal/engine/native_resume_test.go`: the native-resume test named in Acceptance criteria,
  using a real git repo fixture (`workspace: write` is what gives session resumption a stable
  directory across attempts — 04-agents.md's "path-keying trap").
- `internal/archtest`: `TestUI_everyCallHasCLICounterpart` extended coverage (no new test, existing
  one now walks two more routes).

## Benchmarks

None. Conversation traffic is human-paced by construction (decision #7's doc comment states this
explicitly), not a durability-sensitive hot path at L02's scale.

## Migration

None from a prior version.

## Future work

- **Project/task-level Conversations spanning multiple runs**, matching `09-cli-and-tui.md`'s
  mockup exactly ("3 runs · $2.90"). Needs a new aggregate above Run this codebase does not have
  yet; L14 deliberately scopes to 1:1 Run Conversations as the buildable slice (decision #1).
- **A true wait-position cursor** for `wait: conversation`, if a future workflow needs "resolve
  only on the Nth message" rather than "any new message resolves the current wait" (decision #3).
- **Real per-CLI native-resume argv modelling** (decision #5) — the fresh-invocation vs.
  resume-invocation subcommand-shape question for `codex` in particular, once L08's own deferred
  per-CLI flag probing is built.
- **`actor`/`system`-role Conversation messages** once a future document (the TUI, L15, or web UI,
  L20) needs the engine itself to post run-summary or decision cards into the thread (decision #6).
- The remaining `WaitKind`s `03-workflows.md` names (`human`, `timer`, `poll`, `child-run`) stay
  exactly where L05 left them — logged, not silently dropped, but not made real here. `human` is
  L13's job specifically (`CmdCreateHumanTask`'s own "L13 scope" log line already says so); `timer`/
  `poll` need the persisted timer wheel L05's decision #6 named as a gap this document does not
  close.
