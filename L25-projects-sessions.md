# L25 — Projects, Sessions, real worktrees, tunnel auth, and multi-user attribution

Not a numbered build document — a plain running log, matching L21/L22/L23/L24's style. Built in
response to the user's direct, live-testing-driven feedback after `kairos do`/the web chat/the
TUI composer (L24) landed: "I would like to work with the concept of sessions... define the CWD
that the session will be working on... if we create a new worktree it should update the CWD...
maybe a project would be better... can we just remove the token... I would like a user system
(but no authorization at the moment)."

Two of these are DELIBERATE reversals of prior documented decisions. Both were surfaced to the
user directly, with the exact tradeoff named, before being built — not silent scope creep.

## Documented decisions

1. **Real `git worktree`s for Sessions, reversing ADR 0005** — see the new
   [`adr/0014-worktrees-for-interactive-sessions.md`](adr/0014-worktrees-for-interactive-sessions.md)
   for the full reasoning. Short version: ADR 0005's objections (ref collisions, prune races) apply
   to many concurrent, unattended workflow runs against a shared bare mirror — not to one
   interactive chat session at a time, on its own dedicated branch, off the Project's own real
   checkout. The honest residual risk: a Session's worktree shares ref namespace/config with the
   user's real checkout, so a manual `git branch -D`/config change in the user's own terminal can
   disrupt an active session. `EndSession` removes a worktree with `--force` — any uncommitted work
   in it is discarded, a deliberate, documented consequence of a worktree being real, in-progress
   work rather than a disposable clone.
2. **An optional, explicit no-auth mode for the web UI**, reversing nothing from the ADRs but
   deliberately relaxing L20's always-on token requirement — `KAIROS_WEB_NO_AUTH_ACK` must equal
   `web.RequiredNoAuthAck` exactly (`"yes-i-have-my-own-auth-in-front"`) to bypass the token/cookie/
   bearer check. Auth stays ON by default; this is a narrow, loud escape hatch for a user who
   already has Cloudflare Access (or equivalent) in front of the tunnel, matching
   `RequiredNonLoopbackAck`'s exact established pattern. The Host allowlist and Origin/
   Sec-Fetch-Site checks (DNS-rebinding and cross-site-mutation defenses, not identity checks) stay
   on unconditionally regardless of this flag.
3. **Multi-user attribution, explicitly reversing `10-webui.md`'s "no multi-user/accounts"
   non-goal** — the user asked directly, after this whole project was built without it.
   `internal/identity` is attribution ONLY: a `KAIROS_USER` env var (or OS username fallback) for
   the CLI, an `X-Kairos-User` request header for the web UI, both sent as a courtesy label, never
   a credential. There is no login, no password, no per-user access control — everyone can still
   see and act on everything, exactly as before. `domain.ConversationMessageAppended` gained an
   `Author` field (additive, no schema/fixture break — the field isn't required), and
   `Project`/`Session` rows carry `CreatedBy`.
4. **Projects are a plain durable SQLite table, not a domain-folded aggregate** — same posture as
   L16's `source`/`source_cursor` tables: daemon-owned, never replayed through `domain.Advance`,
   because a Project (a named binding to a real directory) isn't a run-scoped fact. `Session`
   follows the same posture, in its own table, `project_id` foreign-keyed with `ON DELETE SET
   NULL` (deleting a Project doesn't corrupt an existing Session's history, it just orphans the
   reference).
5. **A Session elevates `kairos do --continue`'s run-chained continuation into a first-class,
   addressable identity** — rather than replacing it. `--continue <runID>` remains a lower-level
   escape hatch for continuing one specific run's chat without ever creating a Session.
   `--session <id>` takes priority when both are supplied. A Session's `ConversationRunID` is set
   once (the first ad hoc run ever created for it) and never overwritten — every later turn's
   message and reply land in that same run's Conversation, even though (per L24's own documented
   constraint) each turn is still its own new ad hoc run under the hood.
6. **The native LLM session id a turn actually minted is captured via a short, bounded poll (2s),
   not a blocking wait for the whole turn** — `llm.session.started` is appended by
   `dispatchLLMActor` well before the turn's real work finishes, so this is a short, cheap wait, not
   a proxy for the actor's full response time. If it never appears (a dispatch failure, an engine
   backlog), the next turn simply mints a fresh native session — the same graceful fallback
   `resolveSession` already uses for every other resume-miss in this codebase, not a hang or a
   hard failure.
7. **`WorkDirOverride` lives on `registry.NodeDef`, threaded only into the LLM actor's `WorkDir`**
   — never touching `Dir` (the per-exec scratch dir output.json/schema/logs still live in, and
   `HOME`'s credential isolation still comes from). It takes priority over `workspace: write`'s
   normal reference-clone provisioning entirely when set: a Session is bound to ONE real directory
   for its whole lifetime, not a fresh clone per turn.
8. **A project-less Session still gets a real, pre-created scratch directory** (the daemon
   creates `$KAIROS_HOME/sessions/<id>/` before the Session row is written) rather than an empty
   `WorkDir` — an LLM actor's `WorkDirOverride` must be a real, `chdir`-able directory; there is no
   "no directory" case to fall back to.

## What was built

- `adr/0014-worktrees-for-interactive-sessions.md` (new), `adr/README.md` updated.
- `internal/project` (new package): `Manager` — `CreateProject`/`ListProjects`/`GetProjectByName`,
  `StartSession`/`GetSession`/`ListSessions`/`RecordTurn`/`EndSession`, and the real
  `git worktree add`/`remove` plumbing, routed through `internal/executor/local` (the chokepoint
  law, unchanged).
- `internal/eventstore`: `Project`/`Session` types, `project`/`session` SQLite tables (migration
  `0007_projects_sessions.sql`), and their CRUD methods (`projects.go`) — same pattern as L16's
  `source`/`source_cursor`.
- `internal/identity` (new package): `FromEnv`/`FromRequest`, the `X-Kairos-User` header constant.
- `internal/domain`: `ConversationMessageAppended.Author` (additive field).
- `internal/conversation`: `AppendMessageAs` (author-carrying variant of `AppendMessage`).
- `internal/registry`: `NodeDef.WorkDirOverride` (parsed/defaulted the same way
  `ResumeSessionID`/`ConversationRunOverride` already were), `AdHocOptions.WorkDir`.
- `internal/engine/actor_llm.go`: `WorkDirOverride` takes priority over the `workspace: write`
  clone path when set.
- `internal/web/server.go`: `Deps.NoAuth`, `web.RequiredNoAuthAck`; `cmd/kairos/serve.go` wires
  `KAIROS_WEB_NO_AUTH_ACK`.
- `internal/config`: `WebNoAuthAck`, `KairosUser` config fields.
- `internal/api`: `projects.go`/`sessions.go` (new routes), `do.go` extended with `sessionId`.
- `internal/cli`: `client.go`'s `User` field (sent as the identity header), `CreateProject`/
  `ListProjects`/`StartSession`/`ListSessions`/`DoWithSession`; `project.go`/`session.go` (new CLI
  verb files: `kairos project create/ls`, `kairos session start/ls`); `do.go`'s `--session` flag.
- `internal/web`: `projects.go`/`sessions.go` (new pages + composers), templates, a session picker
  wired into the chat page, nav links.
- `internal/apispec/ops.go`: four new `Op` entries, keeping `TestUI_everyCallHasCLICounterpart`
  and `TestUI_webRoutesResolve` green.

## Real bugs found and fixed

None this pass — the design settled cleanly once the ADR 0014 tradeoff and the async
native-session-id capture (decision #6) were worked out; everything built clean the first time it
compiled and passed its own tests.

## Verification

`go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run` (0 issues), `go test ./...
-race` (full suite, all packages, including the two new `internal/project`/`internal/identity`
packages), `make cross` (4 platforms), `make arch` (all architecture tests) — all clean.

A full manual smoke test against a real running daemon confirmed: `kairos project create` detects
git-backed vs. plain directories correctly; `kairos session start --project <name>` provisions a
real `git worktree` (confirmed via `git worktree list` run from the origin checkout, showing the
session's own branch `kairos/session/<id>`); `kairos do --session <id>` dispatches into that real
worktree directory (the fake CLI's reply literally contained its own real `pwd`), records the
turn's native session id, and bumps `runCount`; `kairos session ls`/`kairos project ls` correctly
show `createdBy` attribution from the real OS username.

## Future work

- The chat page's session picker is a plain `<select>` requiring a full page navigation to switch
  sessions — no live AJAX switching.

### Closed since this document was first written

The three gaps this section used to name are now built (see `L27-session-lifecycle.md`):
`kairos session end` (CLI + web dialog + `internal/project.Manager.EndSession` finally reachable,
not just an internal method nothing called), manual adhoc-definition GC (`registry.GC`, wired into
`kairos doctor --self-check`), and `kairos project create` rejecting a path already bound to
another Project.
