# L26 — Session sandbox fix, conversation continuation, session-centric chat, directory picker

Not a numbered build document — a plain running log, matching L21–L25's style. Built directly in
response to real live-testing feedback after L25 (Projects/Sessions/worktrees) shipped: a
session-bound chat produced no output at all, a second message in the plain Conversation page
silently did nothing, the chat experience didn't match a real session's continuous thread, and
creating a Project meant typing a path blind.

## Documented decisions

1. **The real, most important bug of this batch: Claude Code's own sandbox blocked every
   session-bound turn from ever producing output.** `WorkDirOverride` correctly changed the
   actor's cwd to a Session's real worktree, but Claude Code hard-restricts Bash/Read/Write tool
   access to that cwd tree — it could not reach `KAIROS_OUTPUT`'s path under the ordinary scratch
   dir, failing every time with `permission_denials` and no `output.json`. This was found, fixed,
   tested, and shipped as its own commit (`a77e792`) BEFORE this document's other three items,
   since it silently broke the entire point of Session worktrees. Fix: `claude --add-dir <scratch
   dir>` (verified live via `claude --help`), wired in `internal/engine/llm_argv.go`/`actor_llm.go`
   whenever a node's real cwd differs from its scratch dir. Only `claude`'s sandbox model is
   confirmed to need this; `gemini`/`opencode`/`codex` are not verified either way (see Future
   work).
2. **The plain Conversation page's message box is now smart about which kind of run it's
   talking to** — a hand-authored workflow's real `wait: conversation` node keeps using the
   original dumb-append (`PostConversationMessage`), unchanged, because the engine's own live
   subscription genuinely resolves that wait. A `kairos do`-created run (detected by its
   `trigger.received.TriggerRef` carrying the literal `"do:"` prefix `internal/api/do.go` always
   uses) has no such waiting node, so a message posted there now routes through the SAME `POST
   /do` continuation path — via the owning Session if one exists (found by scanning
   `ListSessions` for a `ConversationRunID` match), or by run id if it's a plain, session-less ad
   hoc chat. No new continuation mechanism was built; this reuses `handleDo`'s existing logic
   through the client, exactly as the plain `/chat` page already does.
3. **A real session-centric chat page (`/sessions/{id}`), not a two-step picker.** The prior
   `/chat?session=X` flow required first submitting a "use this session" form, then typing a
   message in a SEPARATE form whose `sessionId` hidden field only had a value if that first step
   was completed — skip it, and the message silently became a plain ad hoc chat with no
   `WorkDirOverride` at all. That silent drop is exactly what produced "it replied that's an empty
   folder": the user picked a session, typed a message, and the request that actually ran carried
   no session id whatsoever. The new page fixes this by construction: the session id lives in the
   URL PATH, not a form field — there is no way to submit a message without it (confirmed by a
  dedicated regression test driving the exact multi-turn sequence a real user hit live: load the
  page, send a message, reload the page — the post-redirect state a second message is actually
  sent from — send a second message; both turns carry the correct session id, where the old
  hidden-field design had silently dropped it on a real live request). It renders the
   session's ENTIRE conversation history (every turn, since every turn's message and reply already
   land in one `Session.ConversationRunID`, per L25/L24), live via the same SSE-fragment pattern
   the run detail and plain chat pages already use. The plain `/chat` page is untouched and still
   valid for a one-off, session-less ad hoc task — the user explicitly said they still want that
   for flows they wouldn't want to bind to a persistent session.
4. **`Session.ConversationRunID` is now exposed over the wire** (`internal/api/sessions.go`'s
   `sessionResponse`, `internal/cli.Client.Session`) — it existed in `eventstore.Session` since
   L25 but was never serialized anywhere a caller outside the daemon could read it. Needed for the
   session page to find its own conversation, and for the Conversation-page fix to find a run's
   owning session.
5. **A new `GET /sessions/{id}`** (`kairos session show <id>`) — a single session's own record,
   without scanning the full list. Real Op entry, real CLI verb, matching this project's parity
   discipline exactly.
6. **The directory browser (`GET /fs/browse`) is deliberately narrow** — it lists one directory's
   immediate real subdirectories (resolving symlinks to decide if they're really directories,
   silently skipping a broken symlink rather than erroring the whole listing), flags git-backed
   entries and dotfile entries, and defaults to the user's home directory when no path is given.
   It does NOT filter dotfiles server-side — every entry is returned with a `Hidden` flag, and the
   web UI's own fragment template is what hides them by default; this keeps the API introspectable
   (`kairos fs browse` on its own is a real, complete listing) without needing a second query
   parameter just to toggle visibility. No new access-control concept was added: this exposes
   nothing an operator with socket access couldn't already see via `ls` — matching AGENTS.md's own
   "the host is the sandbox, and there isn't one" framing for a single-operator local tool.
7. **`kairos fs browse` gets a real CLI verb** even though a human at a terminal would normally just
   type a path directly — kept for parity discipline consistency (every daemon capability this
   project has ever added gets a real CLI counterpart) and because `kairos -o json fs browse` is a
   genuinely useful scriptable listing.

## Public interfaces

```go
// internal/api (sessionResponse gains one field)
ConversationRunID string `json:"conversationRunId,omitempty"`

// GET /sessions/{id} -> sessionResponse

// internal/api/fs.go (new)
type fsEntry struct { Name, Path string; IsGit, Hidden bool }
type fsBrowseResponse struct { Path, Parent string; Entries []fsEntry }
// GET /fs/browse?path=<dir>

// internal/cli.Client (new/extended)
func (c *Client) GetSession(ctx context.Context, id string) (Session, error)
func (c *Client) BrowseFS(ctx context.Context, path string) (FSBrowseResponse, error)
type Session struct { ...; ConversationRunID string }
type FSEntry struct { Name, Path string; IsGit, Hidden bool }
type FSBrowseResponse struct { Path, Parent string; Entries []FSEntry }

// internal/engine/llm_argv.go (extended)
type llmInvocation struct { ...; extraDir string } // --add-dir when cwd != scratch dir
```

## Files to create

```
internal/api/fs.go  fs_test.go
internal/web/session_chat.go  session_chat_test.go
internal/web/fsbrowse.go
internal/web/templates/sessionchat.gohtml
internal/web/templates/frag/fsbrowse.gohtml
internal/cli/fs.go

# modified:
internal/engine/llm_argv.go  actor_llm.go  llm_argv_test.go
internal/api/sessions.go  server.go
internal/apispec/ops.go
internal/cli/client.go  session.go  root.go
internal/web/mutations.go  server.go  testdaemon_test.go
internal/web/templates/projects.gohtml  frag/sessionrow.gohtml
```

## Data changes

None beyond L25's schema — `ConversationRunID` already existed on the `sessions` table; this
document only exposes it over the API/CLI wire, and adds no new columns or tables.

## Acceptance criteria

- `go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run` clean.
- `go test ./... -race` green across every package, including five new regression tests in
  `internal/web` (conversation-box continuation, both the session-owned and session-less cases,
  the no-regression case for hand-authored workflows, the session page's full-thread rendering,
  and the session-send path's URL-path-sourced session id) and two new real-filesystem tests in
  `internal/api` (directory listing with real git-detection and dotfile-flagging, home-directory
  default).
- `make cross` (4 platforms) and `make arch` (9 architecture tests, including
  `TestUI_everyCallHasCLICounterpart` and `TestUI_webRoutesResolve`) clean.
- The exact real-world failure the user hit — a session-bound `kairos do` chat producing no
  output — was reproduced against the live daemon, root-caused to Claude Code's own sandbox, fixed
  with `--add-dir`, and re-verified against the SAME real session/worktree, producing correct
  output.

## Tests

- `internal/engine/llm_argv_test.go`: `TestBuildLLMArgv_claudeSessionWorkDirAddsExtraDir`,
  `TestBuildLLMArgv_claudeNoWorkDirOverrideOmitsAddDir`.
- `internal/web/session_chat_test.go`: the five tests named in Acceptance criteria above.
- `internal/api/fs_test.go`: `TestFSBrowse_listsRealSubdirsDetectsGitAndHidesDotfiles`,
  `TestFSBrowse_defaultsToHomeDirWhenPathOmitted`.

## Benchmarks

None. Nothing here touches L02's durability-sensitive hot path.

## Migration

None from a prior version.

## Future work

- `gemini`/`opencode`/`codex` may have their own sandbox restrictions equivalent to claude's — none
  of the three has been verified either way against a `WorkDirOverride`d session. Registered
  honestly rather than assumed fixed by association.
- The directory browser has no pagination/lazy-loading for a directory with thousands of entries —
  fine for a project root, not stress-tested at scale.
- No "recent paths" memory for the picker — every browse starts from the home directory.
- The session page has no way to end/delete a session from the web UI yet (`kairos session end`
  exists only... actually does not exist yet either — `internal/project.Manager.EndSession` is a
  real, tested method with no CLI verb or web route calling it. A real, named gap.
