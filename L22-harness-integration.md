# L22 — real LLM harness integration

Not a numbered build document (the plan ends at L20, L21 is the hardening pass) — a running log,
same shape as L21, closing NL-29 ("real per-CLI command shapes and flag probing"), named as
Future work in `L08-actor-sdk.md`. Before this pass, `dispatchLLMActor` invoked every actor kind
identically: one configured binary, the file contract (`KAIROS_OUTPUT`/`KAIROS_SCHEMA` env vars,
prompt on stdin), and nothing else — no CLI ever saw a flag telling it to run non-interactively.

**Environment for this pass**: `claude` 2.1.236 (Claude Code), `gemini` 0.22.5 (Gemini CLI), and
`opencode` 1.18.11 are genuinely installed and authenticated here — every claim below marked
"verified live" was run for real against these binaries, not assumed from `--help` text alone.
`codex` and `aider` are **not installed** in this environment; codex's argv shape is transcribed
only from `04-agents.md`'s documented spec and is **unverified** — flagged as such everywhere it
appears, per this project's honesty discipline (documented Future work / NL-* limitations rather
than a false claim of test coverage).

## 1. Per-actor-kind argv builder (`internal/engine/llm_argv.go`, new file)

Replaced the one-size-fits-all invocation with a small table —
`llmArgvBuilders map[string]func(llmInvocation) []string` — one function per CLI kind, matching
this codebase's existing style for small per-kind tables (`nativeResumeArgv` was the same shape,
just narrower; it's now folded into this table). `buildLLMArgv` looks up the actor kind and
returns `nil` for anything not in the table (`"local"`, L08's placeholder CLI kind, and any future
unregistered kind) — exactly this repo's pre-NL-29 behaviour, so the existing fake-CLI test suite
needed no changes to its *behaviour*, only to one test's argv assertion (see §5).

Shapes, each with the live command that proved it:

- **claude**: `--print` (headless — `claude --help`: "starts an interactive session by default,
  use -p/--print for non-interactive output"), `--output-format json` (this engine reads only the
  final `output.json` file, never a stream — `stream-json` would buy nothing and need a parser
  this engine deliberately doesn't have), `--permission-mode acceptEdits`, and exactly one of
  `--session-id <minted-id>` (fresh) or `--resume <prior-id>` (native resume) — never both.
  Verified live: `echo '...' | claude -p --session-id <uuid> --output-format json
  --permission-mode acceptEdits` ran a Bash tool call to completion with
  `"permission_denials":[]` and no prompt; a follow-up `claude -p --resume <that uuid>` recalled a
  fact ("echo hi123") stated only in the first invocation.
- **gemini**: `-o json`, **no positional prompt argument** — verified live that gemini reads stdin
  as the prompt when no positional query is given (`echo '...' | gemini -o json` sent the piped
  text as the prompt; it reached a real auth-configuration error deeper in gemini's own code,
  proving the invocation shape itself was accepted). gemini's `--resume` only accepts `"latest"` or
  a numeric list index (`gemini --help`) — never a caller-chosen id — so there is no native resume
  wiring for gemini, matching `04-agents.md`'s own statement that Gemini has no native resume and
  Stage 3 (extraction) is its path instead.
- **opencode** (new actor kind, see §2): `run --format json`, no positional message — verified live
  that `opencode run` reads stdin as the message (`echo '...' | opencode run --format json`
  answered the piped prompt) and that a Write-tool file edit completed with no permission prompt in
  this non-interactive mode. opencode's `--session <id>` genuinely resumes a **specific** session —
  verified live, a second, separate `opencode run --session <id>` recalled a fact ("the secret
  number 42") stated in an earlier, unrelated invocation — but only an id **opencode itself minted**
  and reported back inside the `--format json` event stream, never one this engine chooses up
  front. Since this engine's documented decision is to read only the final `output.json` file and
  never parse that stream (see `reapLLM`'s doc comment), there is no way for it to learn opencode's
  self-assigned session id at all, so opencode has no native resume wiring either — an honest
  limitation, not an oversight, and distinct from gemini's reason for the same gap.
- **codex** (unverified — not installed here): `exec --json --sandbox workspace-write
  --output-schema <schemaPath> --output-last-message <outputPath>` transcribed from
  `04-agents.md`'s documented example, pointing `--output-schema`/`--output-last-message` at the
  exact same files every other kind already gets via `KAIROS_SCHEMA`/`KAIROS_OUTPUT`, so codex's own
  constrained decoding (`04-agents.md`'s Stage 0) layers on this engine's file contract at no extra
  cost. Resume is `exec resume <id>`, identical to this repo's pre-existing (already-documented)
  codex resume shape.

`nativeResumeSupported(actorKind)` is `true` only for claude and codex — the two kinds that accept
a caller-chosen session id up front. This is consulted both when the engine dispatches attempt 2 of
a `sessionAffinity: node/run` retry (pre-existing mechanism, L14) and by the repair turn (§3, new).

## 2. `opencode` wired as a real actor kind

Added `"opencode": true` to `internal/registry/defaults.go`'s `agentActors` map,
`internal/engine/admission.go`'s `llmActorKinds` map, and the `"claude", "codex", "gemini", ...`
case in `internal/engine/dispatch.go`'s `runActorDispatch` switch — the three places
`claude`/`codex`/`gemini` were already enumerated together, kept in lockstep by cross-referencing
comments in each. No other registry wiring was needed: `requiresOutputSchema` and the retry
defaulter (`defaultRetry`) key off `agentActors`/actor-name checks that already generalise to any
new entry in that one map, matching the existing claude/codex/gemini pattern exactly.

New registry test (`internal/registry/opencode_test.go`) proves an opencode node gets the same
write-node retry upgrade (`MaxAttempts: 2`, `FreshWorkspace: true`) as a claude node, the same
output-schema requirement (rejects a node with neither `output:` nor `outputSchema:`), and that
`sideEffectFree: true` still resolves `RestartPolicy` to `RestartRerun` regardless of actor kind.

## 3. Real bug: minted session ids were never valid for Claude's `--session-id`

`resolveSession` (`internal/engine/actor_llm.go`) minted every LLM session id via
`ulid.Make().String()` — a 26-character Crockford base32 string (e.g.
`01ARZ3NDEKTSV4RRFFQ69G5FAV`). Verified live that Claude Code's real `--session-id` flag validates
its argument's *shape*: `claude -p --session-id not-a-uuid` is rejected outright with `Error:
Invalid session ID. Must be a valid UUID`, and a ULID string is never UUID-shaped (letters like
`R`/`Z`/`T` aren't valid hex, and there are no dashes). Every session this engine minted for a
claude-actor node would have been rejected the moment real per-CLI argv shaping tried to pass it as
`--session-id` — this bug was latent and untriggered before this pass only because the old code
never actually passed a session id to the CLI at all.

Fixed with `newSessionID()`: mints the same `ulid.Make()` 16 bytes (keeping the time-ordering
property) but renders them in UUID shape (`8-4-4-4-12` hex groups) instead of ULID's own encoding.
Verified live that this shape is accepted: `claude -p --session-id 019bf3c1-8a11-7c2e-9f04-2d55aa9e1b31
--output-format json --permission-mode acceptEdits` ran successfully and `session_id` in its JSON
result echoed the same value back.

## 4. Real gap closed: the Stage 2 repair turn never actually resumed

`repairTurn` (`internal/engine/actor_llm.go`) is `04-agents.md`'s Stage 2 — "one repair turn,
in-session" — documented with the example `claude -p --resume $SID`. Before this pass it called
`startLLM(..., resumeOf="")` unconditionally: every repair invocation was a **brand-new session**
with zero continuity from the failing attempt, silently contradicting both the doc and the
function's own doc comment ("runs the CLI exactly once more, in the same workDir/session"). This
was invisible before because no real per-CLI resume flag existed yet for the fresh-invocation path
to omit in the first place.

Fixed: `repairTurn` now takes `sessionID` and sets `resumeOf = sessionID` when
`nativeResumeSupported(actorKind)` is true, so a claude (or documented-spec codex) repair turn
genuinely resumes the just-failed session via `--resume` — the model sees its own prior turn and
the validation errors, and only needs to fix the file. For gemini/opencode/local (no native resume
capability), the repair turn is unchanged: a fresh invocation with no continuity, same as before
this pass — matching `04-agents.md`'s own framing that Stage 2 is available only where a runner can
actually resume.

## 5. Test updates for the new argv shape

`internal/engine/native_resume_test.go`'s fake CLI previously checked `[ "$1" = "--resume" ]` —
valid only because the old code emitted *no other flags at all*, so `--resume` always landed at
position 1. Real claude argv now leads with `--print --output-format json --permission-mode
acceptEdits` before `--resume`, so the fake script now scans all of `"$@"` for `--resume` followed
by a non-empty value, rather than checking a fixed position. No production behaviour change here —
this is strictly the test catching up to the new (correct) argv shape.

## 6. New tests

- `internal/engine/llm_argv_test.go` (package `engine`, unexported access): per-kind argv assertions
  for claude (initial + resume, mutually-exclusive `--session-id`/`--resume`), gemini (no positional
  prompt, no resume flags ever), opencode (no positional message, no resume flags ever), and codex
  (both shapes, explicitly labeled spec-only/unverified in the test's own doc comment); an
  "unknown/local kind gets zero extra flags" regression test; a `nativeResumeSupported` table test;
  and a `newSessionID` UUID-shape regression test (the exact bug from §3) plus a distinctness check.
  All of these are pure-function tests over argv construction — **no real LLM CLI is invoked in the
  automated suite**, matching this project's existing discipline (`writeFakeLLM`'s doc comment).
- `internal/registry/opencode_test.go`: the registry-wiring tests described in §2.

## Verification

- `go build ./...`, `go vet ./...`, `gofmt -l .` — clean.
- `golangci-lint run` — clean (one `staticcheck` SA4000 finding caught during development —
  `newSessionID() == newSessionID()` read as a tautology to the linter — fixed by binding both
  calls to named locals before comparing; not a production bug, a test-only lint fix).
- `go test ./... -race` — full suite green, including the pre-existing
  `TestEngine_llmActorUsesNativeResumeFlagOnSecondAttempt` (now scanning all of argv, §5) and every
  new test in §6.
- `make cross` (darwin/linux × amd64/arm64, `CGO_ENABLED=0`) — clean.
- `make arch` — clean.

## Honest scorecard: tested-for-real vs. spec-only

| Actor kind | Argv shape | How verified |
| --- | --- | --- |
| **claude** | `--print --output-format json --permission-mode acceptEdits` + `--session-id`/`--resume` | **Live**, this environment, claude 2.1.236 — non-interactive completion, no permission prompt, real session resume recalling a prior fact |
| **gemini** | `-o json`, stdin prompt | **Live**, this environment, gemini 0.22.5 — stdin-as-prompt confirmed; the call itself reached a real (unrelated) auth-config error deeper in gemini, proving the invocation shape was accepted |
| **opencode** | `run --format json`, stdin prompt | **Live**, this environment, opencode 1.18.11 — stdin-as-prompt, non-blocking file write, and genuine session resume all confirmed by running real commands |
| **codex** | `exec --json --sandbox workspace-write --output-schema ... --output-last-message ...` | **Unverified** — codex is not installed in this environment; transcribed from `04-agents.md`'s documented spec only, never run against a real binary |

This is a strict reduction in unverified surface from before this pass, not a claim that
everything is now proven: opencode's and gemini's *lack* of native resume is itself only as solid
as the `--help` text and the live experiments above found it to be, and codex remains entirely
untested. A later pass with a codex binary available should re-run this same live-verification
exercise before removing the "unverified" label from `codexArgv`.

## 7. Real end-to-end smoke test

Everything above verified `buildLLMArgv`'s output — pure-function tests, no real CLI, per this
project's own testing discipline. It never verified that a real `kairos` binary, talking to a real
daemon over the real unix socket, actually gets a real `claude` process from spawn to a recorded
`succeeded` Run. The user asked for that explicitly ("otherwise I can't test"), so this section is
that test, run for real, once, outside `go test ./...`'s normal set.

**The workflow** (`cmd/kairos/testdata/real-llm-smoke.yaml`), one `claude` node, the cheapest prompt
that still exercises the real file contract (a real Bash tool call, not a canned reply):

```yaml
name: real-llm-smoke
nodes:
  - id: hello
    actor: claude
    prompt: |
      Run exactly this Bash command and nothing else, then stop:

        echo '{"ok": true}' > "$KAIROS_OUTPUT"

      Do not read or modify any other file. Do not print the JSON in your
      reply.
    output:
      ok: "bool!"
```

**The real command** (also `make smoke-llm`, see below):

```
KAIROS_REAL_LLM_SMOKE=1 go test ./cmd/kairos/ -run TestRealLLMSmoke_Claude -v
```

which builds the real `kairos` binary, points `KAIROS_LLM_BINARY` at the real `claude` on `PATH`
(2.1.236, this environment), `kairos run`s the workflow above against a real daemon it auto-starts,
polls real `kairos show` until the Run reaches a terminal status, then runs real `kairos db verify`.

### First real attempt: a genuine bug, not a wiring mistake in the test

The first real run failed for real, with real information worth keeping: `kairos show` reported
`"Status": "failed"`, and the scratch dir's captured process output showed why —

```
{"is_error":true, ... ,"result":"Not logged in · Please run /login", ...}
```

exit code 1. This is `dispatchLLMActor`'s own per-run `HOME` isolation (04-agents.md: "the
highest-value single line here") doing exactly what it is designed to do — the child never sees
`~/.claude.json` or `~/.claude/.credentials.json`, because those live under the *real* `$HOME`, and
this run's `$HOME` was a fresh, empty scratch directory. Verified directly against the bare CLI,
outside Kairos entirely, to confirm the cause before touching any code:

```
$ HOME=$(mktemp -d) PATH=/usr/bin:/bin:/usr/local/bin:... claude -p --session-id <uuid> \
    --output-format json --permission-mode acceptEdits <<< '{"ping":true}'
{"is_error":true, ... ,"result":"Not logged in · Please run /login", ...}   # exit 1

$ CFGDIR=$(mktemp -d); cp ~/.claude/.credentials.json ~/.claude.json "$CFGDIR"/
$ HOME=$(mktemp -d) CLAUDE_CONFIG_DIR="$CFGDIR" PATH=... claude -p --session-id <uuid> \
    --output-format json --permission-mode acceptEdits <<< '{"ping":true}'
{"is_error":false, ... ,"result":"pong", ...}   # exit 0
```

`CLAUDE_CONFIG_DIR` — 04-agents.md's own documented env var for exactly this
(`CLAUDE_CONFIG_DIR=~/.kairos/agents/claude/backend-engineer`, alongside `CODEX_HOME` for codex) —
was never wired into `startLLM`'s constructed environment. This is a real dispatch bug, in scope for
this pass (the whole point of "real harness integration" is that a real, authenticated CLI must be
able to actually run), not a case of the smoke test needing a hack: **every** claude/codex node this
engine has ever dispatched would authenticate only by accident, if the per-run scratch `$HOME`
happened to already contain credentials, which it never does.

**The fix**: `engine.Config.LLMConfigDir` (env `KAIROS_LLM_CONFIG_DIR`), plumbed through
`internal/config`, `cmd/kairos/serve.go`, and a new small per-kind table in `llm_argv.go`
(`llmConfigDirEnvVar` — `claude` → `CLAUDE_CONFIG_DIR`, `codex` → `CODEX_HOME`, both transcribed
directly from 04-agents.md, nothing invented) consulted by `startLLM` via the new `configDirEnv`
helper. Empty (the default) reproduces the old behaviour exactly — no regression for anyone not
using this new config. `gemini`/`opencode` get no entry: no such env var is documented anywhere in
this repo or was found live during this pass, so the gap is left honest rather than guessed (see
NL-50 in `11-limitations.md`). Pure-function regression test: `TestConfigDirEnv` in
`llm_argv_test.go`.

### Second real attempt: success

```
KAIROS_REAL_LLM_SMOKE=1 go test ./cmd/kairos/ -run TestRealLLMSmoke_Claude -v
```

with `KAIROS_LLM_CONFIG_DIR` now pointed at this environment's real, already-authenticated
`~/.claude`. Real output, verbatim:

```
=== RUN   TestRealLLMSmoke_Claude
    real_llm_smoke_test.go:87: kairos run output: {
          "runId": "01M0HVG3M4M7G3M8Z577YYD1R1",
          "status": "running"
        }
    real_llm_smoke_test.go:125: final kairos show: {
          "ID": "01M0HVG3M4M7G3M8Z577YYD1R1",
          "Status": "succeeded",
          "Executions": {
            "hello": [
              {
                "ExecID": "hello#a1.i1",
                "NodeID": "hello",
                "Status": "succeeded",
                "Attempt": 1,
                "Iteration": 1
              }
            ]
          }
        }
    real_llm_smoke_test.go:137: kairos db verify: {
          "mismatchedRunIds": null
        }
--- PASS: TestRealLLMSmoke_Claude (18.10s)
PASS
ok  	github.com/williamokano/kairos/cmd/kairos	18.100s
```

A real `claude` process, spawned by a real `kairos` daemon over a real socket, made a real Bash tool
call under `--permission-mode acceptEdits` with no prompt (matching §1's own live finding), wrote a
real `output.json`, was reaped, schema-validated, and folded into a `succeeded` Run — replayed
cleanly (`db verify`: `mismatchedRunIds: null`). Eighteen seconds, one real invocation, on the order
of a few cents of API spend.

### Re-running this yourself

- `make smoke-llm`, or directly: `KAIROS_REAL_LLM_SMOKE=1 go test ./cmd/kairos/ -run
  TestRealLLMSmoke_Claude -v`.
- Requires a real `claude` binary on `PATH`, already logged in (`claude /login`) — the test
  `t.Skip`s (not fails) if either the binary or its `~/.claude/.credentials.json` is missing, so it
  never breaks a clean checkout with no `claude` installed.
- **Not** reachable from `go test ./...`, `-race`, or `make test`/`make race` — confirmed by running
  the bare `go test ./cmd/kairos/... -run TestRealLLMSmoke_Claude` (no env var) and observing
  `--- SKIP`, and separately confirming the full `go test ./... -race` run this pass's Verification
  section reports contains no real LLM invocation (`KAIROS_REAL_LLM_SMOKE` is never set by anything
  in this repo's normal test/CI path — `grep -r KAIROS_REAL_LLM_SMOKE` outside this file and the test
  itself returns nothing).
- Respects `CLAUDE_CONFIG_DIR` if already set in your shell (uses it as-is); otherwise defaults to
  `~/.claude`.

## 8. SSE live clients: TUI push, and `kairos logs --follow`

Closes two gaps that were named, not silently dropped, in their own build documents:
`L15-tui.md`'s Future work #1 ("Real SSE-push live updates, replacing the 2-second poll") and
`L04-daemon-api-cli.md`'s deferred `kairos logs --follow` ("the SSE plumbing already exists via
`GET /events`"). Both close against the *same* daemon-side machinery — `GET /events`
(`internal/api/events.go`), proven since L04 and reused unchanged by L14/L18 — so this pass is
entirely about real **clients**, not new server surface. Neither `internal/api/events.go` nor its
resumption contract (`?after=`/`Last-Event-ID`, ADR 0010) changed at all.

### What was built

**`internal/cli.Client`, two new methods** (`client.go`):

- `StreamEvents(ctx, streamID, afterSeq, onConnected, onEvent)` — one indefinite attempt at
  `GET /events`, using a dedicated `http.Client{Transport: c.http.Transport}` rather than the
  package's own `c.http` (see the real bug below for why that distinction is load-bearing). Shares
  `scanSSEBody`, a small refactor pulled out of the pre-existing `Events` method so the wire format
  is decoded in exactly one place instead of two near-identical copies.
- `FollowEvents(ctx, streamID, afterSeq, onEvent, onStatus)` — wraps `StreamEvents` in a reconnect
  loop with capped exponential backoff (500ms → 10s), resuming from the last envelope's `GlobalSeq`
  on every reconnect. This is the actual "handle reconnection/resumption correctly" mechanism both
  clients below use; `onStatus` reports `FollowConnecting`/`FollowConnected`/`FollowDisconnected`
  transitions so a caller can surface "reconnecting..." instead of going silent.

**`internal/tui/sse.go`, new file**: `sseSubscription` bridges one background goroutine (running
`Client.FollowEvents` against every stream, unfiltered) into bubbletea's synchronous `Update` loop
via a buffered channel plus a `waitForSSE` `tea.Cmd` that blocks on a channel receive and is
re-issued after every message — the same read-one-then-reissue pattern bubbletea's own docs use for
a channel-fed external event source. `internal/tui/model.go`'s `tea.Tick`-based poll
(`tickMsg`/`tickCmd`/`refreshInterval`) is gone outright, not merely supplemented: `Model.Init` now
starts `waitForSSE` alongside the first fetch, and every subsequent screen refresh is triggered by
an `sseEventMsg` arriving, not by a timer. Deliberately unfiltered by stream — every screen's
refetch was already cheap, and re-running it once per incoming envelope, on one local daemon's
event volume, is the same "three orders of magnitude below where it would matter" cost AGENTS.md
already accepts for SQLite write volume — so no per-screen event-routing logic was needed to get
this right.

**`internal/cli/logs.go`, new file**: `kairos logs <runID>` (bounded historical read, reusing the
pre-existing `Events` method) and `kairos logs <runID> --follow` (indefinite live tail via
`FollowEvents`, ending cleanly on Ctrl-C via `signal.NotifyContext(ctx, os.Interrupt)` rather than
an abrupt kill). `-o json` prints each envelope as a JSON line; the table form is
`<GlobalSeq> <StreamID> <EventType> <payload>`. `apispec.Ops` gains one entry —
`{GET /events -> CLIVerb: "logs"}` — no new route, matching the deferred item's own framing that
the plumbing already existed; `TestUI_everyCallHasCLICounterpart` stays green with this single
addition.

### Real bugs found

1. **The package `http.Client`'s 30s `Timeout` would have silently truncated every live tail past
   30 seconds.** `cli.NewClient` sets `http.Client{Timeout: 30 * time.Second}` — a sensible default
   for the bounded request/response calls every other verb makes, but that `Timeout` bounds the
   *entire* request including reading the body, and would sever `kairos logs --follow` (and the
   TUI's subscription) after exactly 30 seconds of otherwise-healthy streaming, indistinguishable
   from a network failure. Caught during design, before it could be an intermittent field bug: fixed
   by giving `StreamEvents` its own `http.Client{Transport: c.http.Transport}` — same unix-socket
   dialer, no `Timeout` — so only `ctx` cancellation ends a live connection.
2. **`internal/cli` cannot import `syscall`.** The first cut of `kairos logs --follow`'s Ctrl-C
   handling used `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)`, matching
   `cmd/kairos/serve.go`'s daemon-shutdown pattern. `TestArchitecture_noExecOutsideExecutor` failed
   immediately and correctly: only `internal/executor/local`, `internal/executor/exectest`, and
   `cmd/kairos` may import `syscall`/`os/exec`/`golang.org/x/sys`. Fixed by catching `os.Interrupt`
   only (SIGTERM handling stays where it belongs, in the daemon's own lifecycle in `cmd/kairos`) —
   a real instance of an architecture test doing exactly its job, not a fixture drill.
3. **No `node.execution.succeeded` event type exists.** The first draft of both new end-to-end tests
   (below) waited for that event type by name, assumed by analogy with
   `node.execution.failed`/`.lost`/`.interrupted`. It does not exist: a node's success is represented
   by `node.output.received` (schema-valid output), with "succeeded" a *projected* status, never its
   own event. Both tests failed against a real daemon on the first run — exactly the kind of
   assumption a real end-to-end test catches that a mocked one would not — and were corrected to
   watch for `node.output.received`.

### Tests

- `internal/cli/client_internal_test.go` (new, `package cli` — an internal test file, needed to
  construct a `Client` pointed at an `httptest.Server` rather than a real unix socket):
  `TestClient_FollowEvents_reconnectsAndResumesWithoutGapOrDuplicate` runs a fake `/events` server
  that drops the connection after one envelope, and asserts the reconnect's `?after=` names exactly
  that envelope's `GlobalSeq` (no gap, no duplicate) and that both `FollowConnected`/
  `FollowDisconnected` status transitions fire.
  `TestClient_StreamEvents_stopsOnContextCancellation` proves the other half of the contract:
  cancelling `ctx` against an indefinitely-open connection returns promptly rather than hanging.
- `internal/tui/sse_daemon_test.go` (new):
  `TestSSESubscription_pushesRunEventsAsTheyLandNotOnATimer` is the real end-to-end proof for the
  TUI half — against a real `kairos serve` and a real in-flight node (`internal/tui/testdata/live.yaml`'s
  `n2` sleeps ~1s), it drives the actual `sseSubscription`/`waitForSSE` production code (not a mock),
  and asserts (a) the first push for the newly created run arrives well under 2 seconds — the old
  poll's cadence — and (b) `n2`'s `node.execution.started` and `node.output.received` pushes arrive
  with a real ≥700ms gap between them, which a single batched fetch at the end could not produce.
- `cmd/kairos/logs_follow_test.go` (new): `TestIntegration_logsFollowStreamsAsProduced` is the real
  end-to-end proof for the CLI half — spawns a real `kairos logs <runID> --follow` subprocess
  against a real daemon and the same short-lived-node fixture
  (`cmd/kairos/testdata/logs-follow.yaml`), reads its stdout line by line as it is produced, and
  asserts the same ≥700ms gap between `n2`'s started and output lines, then ends the follow with a
  real `SIGINT` and asserts a clean exit — proving it is genuinely tailing, not dumping once the run
  (and the process) has already finished.

### Verification

- `go build ./...`, `go vet ./...`, `gofmt -l .` — clean.
- `golangci-lint run` — clean (three `errcheck` findings on unchecked `fmt.Fprintf`/`Fprintln`
  return values in the new test/CLI code, and one `staticcheck` QF1003 suggesting a tagged switch
  over an if/else-if on `sseStatusMsg.status` — all fixed, not suppressed).
- `make arch` — clean, and this pass is one of the rare ones where a real (non-fixture) architecture
  violation was caught and fixed live — see real bug #2 above.
- `go test ./... -race` — full suite green, including every new test above under `-race`.
- `make cross` (darwin/linux × amd64/arm64, `CGO_ENABLED=0`) — clean.

No document's design was proven wrong by this pass — `09-cli-and-tui.md`'s SSE-plus-POST realtime
design (ADR 0010) is exactly what got implemented, on schedule with what both build documents' own
Future work sections said was deferred, not foreclosed.
