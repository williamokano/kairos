# L24 — `kairos do`: a real free-text entrypoint, on all three surfaces

Not a numbered build document (outside the original L00–L20 plan) — a plain running log, matching
`L21-hardening.md`/`L22-harness-integration.md`/`L23-webui-revamp.md`'s style. The user explicitly
asked for this after hitting the exact gap it closes: `09-cli-and-tui.md` and `L15-tui.md` both
named "start a run from prose" as real, wanted, and unbuilt — this document builds it, on the CLI,
the web UI, and the TUI, in one pass.

## What was built

1. **`registry.SynthesizeAdHoc`** (`internal/registry/adhoc.go`): takes free text and an actor
   kind, produces a real one-node `Definition` — `actor: <configured default>`, `prompt: <text
   verbatim>`, `workspace: read`, `output: {result: "string!"}`, `postOutputToConversation: true`
   — through the exact same `Parse → ApplyDefaults → Validate` path any hand-authored workflow
   goes through (`LoadBytes`, called before the file is ever written to disk, so a synthesis bug
   fails loudly instead of producing a broken file a later dispatch discovers). Written to
   `$KAIROS_HOME/adhoc/<ulid>.yaml` — the engine re-reads a run's `DefinitionRef` from disk on
   every dispatch (retries, crash recovery, `kairos fork`), so it has to still be there.
2. **Three new, narrow `NodeDef` fields** (`PostOutputToConversation`, `ConversationRunOverride`,
   `ResumeSessionID`) — not general workflow-author features, just the minimum an ad hoc chat
   needs. `PostOutputToConversation` makes `internal/engine`'s `reapLLM` append the real output
   (via a small `result → message → text → whole-object` extraction heuristic) as an `"assistant"`
   Conversation message once it lands.
3. **`POST /do`** (`internal/api/do.go`): the one new daemon endpoint. Synthesizes the definition,
   creates the run through `tasksource.CreateRun` — the *same* one code path every trigger source
   already uses (L16), not a bypass — and immediately posts the user's own text into the
   Conversation as `"human"`.
4. **`kairos do <text> [--continue <runID>]`**, **the web UI's `/chat` page**, and **the TUI Home
   composer** all call this one endpoint. Three real clients of one entrypoint, matching this
   whole project's established multi-surface parity discipline — `TestUI_everyCallHasCLICounterpart`
   and the web-route-parity test (`TestUI_webRoutesResolve`) both stay green with the new
   `apispec.Op` entry.
5. **Multi-turn continuation with a real native `--resume`.**

## Documented decisions

1. **Per-turn new run, not a single continuously-executing run.** A true single-run chat loop
   needs two things this pass does not build: graph-cycle routing (a node's edge pointing back to
   an earlier node) and dynamic `$.conversation.latest`-shaped input binding (a generalization of
   the already-registered NL-37 gap). Investigated directly: `domain.dispatchExec` sends any node
   with a non-nil `Wait` straight to `ExecWaiting` and *never* dispatches `CmdStartNode` for it —
   a node cannot both run an actor and hold a `wait: conversation` clause, so "the same node loops
   on itself" isn't reachable with current primitives. Given that, continuation is honestly scoped
   to: each new message spins up a **new** ad hoc run, whose node carries the **prior** run's real
   session id as `ResumeSessionID` (read off its last `llm.session.started` event) — a genuine
   native `--resume`, proven by `TestKairosDo_continuationResumesTheSameSession`'s fake CLI, which
   refuses to succeed unless invoked with the *exact* prior session id — and whose
   `ConversationRunOverride` targets the *original* run, so every turn's reply still lands in one
   continuous thread. A true single-run loop is Future work.
2. **`ResumeSessionID` bypasses `resolveSession`'s normal derivation entirely**, rather than
   extending `priorSession`'s same-run/same-node scan to search other runs. `priorSession` reads
   one run's own event stream by design; teaching it to search arbitrary *other* runs would widen
   a durability-sensitive read path for a single feature. The explicit field is trusted outright —
   if the resume target is genuinely gone, the real LLM CLI's own `--resume` fails loudly and the
   node fails honestly (AGENTS.md rule 1), not silently.
3. **Default actor is a new `KAIROS_DEFAULT_DO_ACTOR`/`Config.DefaultDoActor` knob**, defaulting to
   `"claude"` — genuinely installed, authenticated, and proven working in this environment
   (`L22-harness-integration.md`'s real smoke test). The mechanism itself has no claude-specific
   knowledge; any actor kind `internal/engine` already dispatches works.
4. **Output-to-conversation text extraction is a narrow heuristic** (`result → message → text →
   the whole object, stringified`), not a general schema-to-prose renderer — the synthesized
   definition always declares `{result: "string!"}`, so the first branch is what actually fires;
   the fallbacks exist only so a hand-authored workflow that opts into
   `postOutputToConversation` against a differently-shaped schema still gets *something* readable.
5. **The web chat page is a new, dedicated `/chat` page**, not a rework of `L20-webui.md`'s
   existing file-path-only home composer — a workflow author's normal "run this file" flow stays
   untouched. It reuses the *existing* real SSE reverse-proxy (`/frag/run/{id}/events`) against the
   conversation's own stream id (`conversation:<runID>` is just another valid stream id to that
   proxy) rather than falling back to a poll — L15-tui.md's own "replace the poll with real push"
   standard, gotten for free.
6. **The TUI's Conversation screen branches on a new `isAdHoc` flag** to decide whether "send" means
   a plain `PostConversationMessage` (a real `wait: conversation` workflow, which stays
   non-terminal while it waits) or `Do(text, continueRunId)` (an ad hoc chat, whose run is already
   terminal after each turn). Set `true` only when reached via `kairos do`'s own `doResultMsg`;
   reset `false` on every other path into that screen, so a stale flag never survives into an
   unrelated real conversation.
7. **A live `kairos do`/web chat/TUI request is exempt from `tasksource.QueueLimits`**, matching
   `kairos run`'s own existing exemption — `maxQueued`/`maxOpenDecisions` backpressure targets
   trigger-created *backlog*, not a human acting right now (L16-triggers.md's own documented
   precedent, extended here rather than re-litigated).

## Real bugs found

None in the new production code this round — every new test passed on first real run once the
design settled. The design work above (tracing `dispatchExec`'s Wait-vs-Actor exclusivity,
`resolveSession`'s same-run scope, `domain.LLMSessionStarted`'s event shape) is what took the
effort; the implementation itself came out clean.

## Tests

- `internal/registry/adhoc_test.go`: synthesis produces a valid, loadable one-node `Definition`;
  continuation carries the resume id and conversation-override target; a missing actor is rejected.
- `cmd/kairos/do_test.go`: `POST /do` end-to-end against a real daemon and a fake `claude` CLI —
  a run is created, the user's message and the actor's real output both land in the Conversation,
  `kairos db verify` stays clean; a second, restart-spanning test proves the continuation's
  `--resume` carries the *exact* prior session id.
- `cmd/kairos/do_cli_test.go`: `kairos do` invoked as a real CLI verb end to end.
- `internal/web/chat_test.go`: the bare composer renders; an existing conversation renders; sending
  posts to the daemon's `/do` and issues both a `Location` and an `HX-Redirect` header (so htmx does
  a real navigation instead of swapping the followed page into a small target); continuation passes
  `continueRunId` and redirects back to the *original* conversation, not the new turn's run.
- `internal/tui/do_test.go`: driving the real Home composer key handling against a real daemon
  (fake CLI) confirms `submitComposer` genuinely calls `POST /do`, and the resulting message
  navigates to `ScreenConversation` with `isAdHoc` set and the real run id wired in.
- `TestUI_everyCallHasCLICounterpart` and `TestUI_webRoutesResolve` both stay green with the new
  `POST /do` → `do` → `POST /chat` mapping.

## Verification

`go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run` (0 issues), `go test ./...
-race` (full suite, every package), `make cross` (darwin/linux × arm64/amd64), `make arch` (all
architecture tests) — all clean.

## Future work

- A genuine single-run chat loop (graph cycles + `$.conversation.*` dynamic input binding) —
  named above as the honest reason continuation is per-turn-new-run instead.
- `$KAIROS_HOME/adhoc/*.yaml` has no GC — these files accumulate forever. `internal/workspace.GC`'s
  existing pattern is the natural model for a future cleanup pass.
- The web chat page's live update still depends on the SSE proxy already built for run events;
  a dedicated conversation-level endpoint (rather than reusing `/frag/run/.../events` with a
  conversation stream id) would be more explicit, if this pattern gets reused elsewhere.
- `gemini`/`opencode` ad hoc chats inherit NL-50's gap (no credential-isolation fix yet for those
  kinds) — only `claude`'s `CLAUDE_CONFIG_DIR` wiring is proven live.
