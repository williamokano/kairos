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
