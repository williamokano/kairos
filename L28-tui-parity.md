# L28 — TUI parity with the web UI's Projects/Sessions feature set

A plain running log (matching L24–L27's style), not a full ten-section build document.

## What was built

The TUI previously had only plain `kairos do` (ad hoc, no session — the Home composer always
called `client.Do(text, "")`), with nothing at all for Projects, Sessions, the session-centric
chat, `kairos session end`, or the directory picker. This closes that gap by adding a THIRD
presentation of daemon endpoints the web UI and CLI already call — no new daemon-side API,
`internal/cli.Client` already had every method needed (`CreateProject`, `ListProjects`,
`StartSession`, `ListSessions`, `GetSession`, `EndSession`, `BrowseFS`, `DoWithSession`).

- **`ScreenProjects`**: a plain list (name/path/git-detected), plus a real create flow — type a
  name, then a genuine directory-tree picker against `GET /fs/browse` (the same endpoint the web
  UI's picker calls). `j`/`k` move, `enter` descends into a subdirectory, `u`/backspace goes to
  the parent, `s` selects the currently-browsed directory and creates the project.
- **`ScreenSessions`**: a list (id/actor/run count/last used), plus a two-field start flow
  (project name, actor — `tab` switches, matching `kairos session start --project --actor`'s own
  shape exactly, no project-picker widget).
- **`ScreenSessionChat`**: the TUI's real answer to the web UI's `/sessions/{id}` page — full
  message history rendered as a thread, a persistent input box, and `x` to end the session
  (a real two-step typed confirmation: a reason, then the session id typed out again, matching
  `kairos session end --reason --confirm <id>`'s own no-`--yes`-shortcut discipline exactly).
- **Command palette**: `ses_`-prefixed input now jumps straight into that session's chat screen
  (`session`/`sessions`/`project`/`projects` also resolve as screen names) — the user's own ask
  for "start chatting in session X" from anywhere, without forcing every interaction through the
  plain ad hoc composer.
- Global keys `p` (Projects) and `s` (Sessions) added to the always-available NAV binding set.

## Documented decisions

1. **The directory picker runs in NAV mode, not INPUT** (`j`/`k`/`enter`/`u`/`s`), matching every
   other list-browsing screen in this codebase — only the project *name* field is a text input.
   This surfaced a real key-collision risk (below), fixed structurally rather than patched key by
   key.
2. **Session-start is two typed text fields (project name + actor), not a project-picker widget.**
   This exactly mirrors `kairos session start`'s own CLI flags — a session binds to a Project by
   name, same as the verb. Building a second picker widget here would be inventing a UI the CLI
   itself doesn't have.
3. **`fetchSessionChat` combines `GetSession` + `GetConversation` into one message type**
   (`sessionChatFetchedMsg`) rather than two round trips through the model's `Update` loop — the
   screen always needs both together (header data + thread), and a session with no turns yet
   (`ConversationRunID == ""`) is a real, valid empty state, not an error.
4. **`CLI-GUIDE.md` was NOT updated.** Every existing entry in that guide is verified by actually
   running the command against a live daemon first — this environment has no real TTY to launch
   bubbletea's full-screen `tea.NewProgram(m, tea.WithAltScreen())` against, so the honest choice
   is to skip the guide update rather than describe untested interactive behavior as if it had
   been run for real. The new screens ARE proven for real, just through the same
   `Model`/`tea.Cmd` layer L15's own test suite already uses to verify TUI behavior without a TTY
   (`buildKairosForSSETest`/`startRealDaemon`, real daemon, real fake-LLM-CLI, no mocking).

## Real bugs found and fixed (during this document's own implementation)

1. **A global-key collision that would have silently swallowed the picker's own keys.** The
   directory picker uses `s` ("select this directory") and relies on `esc` to cancel back to the
   name field — but both are also global NAV-mode bindings (`s` → Sessions screen, `esc` → walk
   back a screen via `m.back()`), and the global switch in `handleKey` runs *before*
   `dispatchScreenKey`. Without a fix, pressing `s` while browsing the picker would have jumped to
   the Sessions screen instead of selecting a directory, and `esc` would have exited all the way
   back to Home instead of just canceling the picker. Fixed with an early, full bypass for the
   picker's active state — the same pattern `ScreenDecision`/`ScreenOnboarding` already use for
   exactly this reason, not a per-key carve-out.
2. **The NAV-mode dispatch for all three new screens was missing entirely on first pass.** The
   routing was added to `handleGlobalInputKey` (the `mode == ModeINPUT` path) but never to
   `dispatchScreenKey` (the `mode == ModeNAV` path) — meaning `j`/`k`/`n`/`x`/`enter` on any of the
   three new screens would have silently done nothing. Caught before writing any tests, by
   re-reading the actual dispatch flow rather than assuming the first edit was complete; fixed by
   adding the three missing cases to `dispatchScreenKey`.
3. **A test assertion bug, not a product bug**: the session-end regression test initially asserted
   `cmd == nil` as a *failure* condition when advancing from the reason step to the confirm step —
   backwards, since a step transition correctly returns no command (it isn't a daemon call yet).
   Fixed the assertion to check the actual state transition (`endStep == 1`) instead.

## Tests

`internal/tui/session_test.go` — four real tests, all against a real running daemon (no mocking,
matching every existing TUI test's convention):
- `TestProjectsScreen_listsRealProjects` — a real project, created via the client, is fetched and
  rendered by the Projects screen.
- `TestSessionsScreen_startsARealSession` — starting a session via the TUI's own state produces a
  real session the daemon's `ListSessions` then reports.
- `TestSessionChat_sessionIDNeverLostAcrossTwoMessagesInTheSameScreen` — the coordinator's
  requested regression test for the exact failure class the web UI hit live: two full send-message
  round trips through the SAME screen instance, confirming `sessionChat.sessionID` never changes
  and the session's real `RunCount` advances by exactly one per message (a dropped session id
  would silently fall back to an unrelated ad hoc run that never touches this count at all).
- `TestSessionEnd_requiresTheFullTypedConfirmationSequence` — proves the destructive `EndSession`
  call is genuinely unreachable without both a real reason and the session id typed out exactly;
  an empty reason, and a present-but-wrong confirm, are both checked to leave the session
  untouched before the final correct sequence is shown to actually end it.

## Verification

`go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run` (0 issues), full
`go test ./... -race` (all packages green), `make cross` (darwin/linux × arm64/amd64), `make arch`
— all clean.

## Future work

- No live AJAX session-switching from within the chat screen itself (matches the web UI's own
  named gap in `L25-projects-sessions.md`) — switching sessions means leaving the screen and
  re-entering via the Sessions list or the palette.
- The directory picker has no pagination for a directory with very many entries, same as the web
  UI's own named gap in `L26-session-chat.md`.
- `gemini`/`opencode`/`codex` sandbox behavior under a session's `WorkDirOverride` remains
  unverified for all three kinds (matches `L26-session-chat.md`'s own honest gap) — this document
  didn't add new verification either, since it's a daemon-side actor concern, not a TUI one.
