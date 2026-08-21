# L27 — session lifecycle: end, GC, and a real path-collision check

A plain running log, matching L21/L22/L23/L24/L25/L26's style, not the full ten-section build-doc
structure — this closes three gaps those documents' own Future work sections named.

## 1. `kairos session end`

`internal/project.Manager.EndSession` already existed and was already tested (it removes a
git-backed session's real worktree and branch) — nothing called it from outside the package. Added:

- `Store.DeleteSession` (`internal/eventstore`) — `EndSession` now also removes the session's own
  row, not just its worktree; leaving a DB record pointing at a directory that no longer exists
  would be exactly the kind of confusing state AGENTS.md rule 1 warns against.
- `DELETE /sessions/{id}` (`internal/api/sessions.go`), requiring a non-empty `reason` and
  `confirm == id` — the SAME server-decides discipline `handleCancelRun`/`handleForkRun` already
  established, applied here rather than trusting the CLI's own already-typed id argument to be
  confirmation enough on its own.
- `kairos session end <id> --reason <text> --confirm <id>` (`internal/cli/session.go`) — deliberately
  no `--yes`/`--force` shortcut, matching every other destructive verb in this codebase.
- A web dialog on the session chat page (`/sessions/{id}`), mirroring the run detail page's
  cancel/fork dialogs exactly (typed-confirm input with `pattern="{{.Session.ID}}"`,
  `data-confirm-submit disabled` by default).

**Real bug found and fixed**: the web dialog's form submits via `DELETE`, and Go's
`http.Request.ParseForm` only reads the request body into `PostForm` for `POST`/`PUT`/`PATCH` — for
`DELETE` it silently leaves `PostForm` empty and never touches the body at all. The handler's reuse
of `requireTypedConfirm` (which reads `r.PostForm`) therefore always saw an empty confirm value,
rejecting every real submission regardless of what the dialog actually sent. Caught by
`TestEndSessionDialog_matchingConfirmReachesTheDaemon` failing with a 422 on the *correct* input.
Fixed by reading and parsing the request body explicitly (`io.ReadAll` + `url.ParseQuery`) instead
of relying on `ParseForm`'s method-gated behavior.

## 2. Adhoc definition GC

`$KAIROS_HOME/adhoc/*.yaml` files (one per `kairos do` turn, per `registry.SynthesizeAdHoc`'s own
doc comment) had no cleanup path at all. Added `registry.GC(homeDir, activeDefRefs, retention,
now)`, mirroring `internal/workspace.GC`'s exact shape (a caller-supplied keep-set, not a name this
function invents itself) plus a second, independent safety net: a file is removed only if it is
BOTH unreferenced by any non-terminal run's `DefinitionRef` AND older than `retention` (7 days).
The age check exists because "not currently active" alone isn't safe — a just-succeeded run's adhoc
file must stay forkable (L18) for a real window after it finishes, not just for as long as it
happened to still be running.

Wired into `Engine.SelfCheck` (`internal/engine/pause.go`) alongside the existing orphan-workspace
GC it already performs live — `kairos doctor --self-check` now reclaims both. Never wired to a
timer (no persisted timer wheel exists yet, matching L05/L19's own precedent) — this is a manual,
on-demand operation, same posture as workspace GC.

## 3. Project path collision detection

`Manager.CreateProject` now scans existing Projects for a matching absolute `RepoPath` before
registering a new one, failing loudly and naming the conflicting Project (id + name) rather than
silently allowing two Projects to alias the same directory — a real correctness risk, since every
session's worktree path is derived from its Project's `RepoPath` (`RepoPath + "-sessions"`), and two
Projects sharing one path would collide on that same sibling directory.

## Tests

- `internal/registry/adhoc_test.go`: `TestGC_removesOnlyInactiveAndOldFiles` — a terminal-and-old,
  terminal-and-recent, and active-regardless-of-age file, confirming exactly the right one is
  removed.
- `internal/project/project_test.go`: `TestCreateProject_rejectsADuplicateRepoPath`.
- `internal/web/mutations_dialogs_test.go`: `TestEndSessionDialog_bypassWithoutMatchingConfirmIsRejected`,
  `TestEndSessionDialog_matchingConfirmReachesTheDaemon` (the test that caught the DELETE-body bug).
- `cmd/kairos/session_end_test.go`: `TestKairosSessionEnd_removesTheRealWorktree` — a real daemon, a
  real git-backed Project, a real provisioned worktree; a bypass attempt is rejected and the
  worktree survives it; the real end removes the worktree from disk AND from `git worktree list`
  run against the origin checkout, and the session record itself 404s afterward.

## Verification

`go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run` (0 issues), `go test ./...
-race` (full suite, every package), `make cross` (4 platforms), `make arch` (all architecture
tests) — all clean.

## Future work

- No automatic/scheduled GC — no persisted timer wheel exists yet; `registry.GC`/`kairos doctor
  --self-check` are manual-invocation only, same as `internal/workspace.GC`'s own precedent.
- Live AJAX session-switching in the chat page's picker remains a plain `<select>` requiring full
  page navigation — cosmetic, out of scope for this pass.
- Directory-browser pagination/recent-paths memory, and `gemini`/`opencode`/`codex` sandbox
  verification against a `WorkDirOverride`d session, remain open gaps from `L26-session-chat.md`,
  unrelated to this pass's scope.
