# L29 — Workflow and trigger-source authoring

A plain running log (not a full ten-section build doc), matching L24-L28's style. Closes two real,
user-confirmed gaps: "how do I create the things — the workflows, the task sources so it can get
the tasks automatically?"

## What was built

1. **`registry.SaveFlow`/`ListFlowDefinitions`/`GetFlowDefinition`** (`internal/registry/flow.go`) —
   a real, hand-authorable counterpart to `SynthesizeAdHoc`'s machine-authored one. Validates via
   the exact same `LoadBytes` path (Parse → ApplyDefaults → Validate) every workflow already goes
   through, BEFORE writing anything durable — a bad workflow is rejected loudly, at save time, with
   the real registry error text, never silently written and discovered broken by a later `kairos
   run`. Written to `$KAIROS_HOME/flows/<name>.yaml`. Refuses path-traversal names and refuses to
   overwrite an existing flow (no update semantics in this pass — see Future work).

2. **`POST /flow-definitions` / `GET /flow-definitions` / `GET /flow-definitions/{name}`**
   (`internal/api/flows.go`) — the daemon-side surface every client (`kairos flow`, the web editor)
   calls through, so a bad workflow gets the identical error message everywhere.

3. **`kairos flow create <name> (--file <path> | --from-stdin)` / `flow ls` / `flow run <name>`**
   (`internal/cli/flow.go`) — `flow run` resolves the saved name to its real path and dispatches
   through the exact same `CreateRun` path a hand-run `kairos run <file>` already uses. No
   special-cased run mechanism.

4. **A real textarea editor on the web Flows page** (`internal/web/flows.go` + `flows.gohtml`) —
   posts to the same `POST /flow-definitions` route, surfaces the real validation error as a
   visible HTML fragment (reusing the composer-bugfix discipline: htmx never swaps a plain-text
   non-2xx body into the DOM, so errors must render as real `<p class="error">` fragments).

5. **`tasksource.BuildCronConfig`** (`internal/tasksource/manager.go`) — the ONE place cron's
   `source.config` JSON shape is constructed, exported so `kairos src add cron`'s friendly flags
   and the web Sources form's fields both produce the identical config `startCron` itself parses.
   Extended `createSourceRequest`/`POST /sources` to accept discrete `schedule`/`weekday`/`hour`/
   `minute` fields as an alternative to hand-writing `--config`'s raw JSON, for `kind: cron` only.

6. **`kairos src add cron ...` friendly flags** (`internal/cli/src.go`) and **a real cron-creation
   form on the web Sources page** (`internal/web/sources.go` + `sources.gohtml`), both building
   config through the one shared constructor.

7. **TUI parity**: `ScreenFlowCreate` (name → local file path this process reads itself → POST,
   reusing the exact save path `kairos flow create --file` uses) and `ScreenSourceCreate` (the
   same discrete cron fields, posted via a new `cli.Client.AddCronSource` that lets the DAEMON
   build the config server-side — see Documented decision #2 below for why). Reachable from the
   command palette: `:flow new`, `:source new`.

## Documented decisions

1. **Scope narrowed to "cron" for friendly source-creation flags, not every named kind.**
   `08-triggers.md` names eight compiled-in source kinds (github, jira, linear, inbox, cron,
   repo-watch, git, shell), but reading `internal/tasksource/builtin.go`'s `Registry` directly
   showed only `"fake"` (test-only) is ever registered — `github`/`jira`/`linear`/a generic NDJSON
   plugin kind have real `Source`-implementing Go types in this tree (`Plugin` in `plugin.go`) but
   are **never constructed anywhere**: `Registry.Build` fails with "unknown source kind" for
   anything but `cron`/`inbox`/`fake`, and `Manager.startPoller` just logs that error and returns —
   a source row for any other kind would silently never poll. Building pretty flags for a kind that
   cannot actually run would be dishonest. `cron` is the one real, working, structured-config kind;
   friendly flags/forms were built for it specifically. `inbox` needs no creation UI (it's a
   singleton daemon-wide watcher, already auto-enabled).
2. **The TUI's cron-source screen posts discrete fields, not a pre-built config string** — unlike
   `internal/cli`/`internal/web` (which both import `internal/tasksource` directly and call
   `BuildCronConfig` themselves), `internal/tui` must never import execution-adjacent machinery,
   even transitively (ADR 0008, `TestArchitecture_tuiHasNoExecution`) — and `tasksource` imports
   `internal/executor/local` (for its NDJSON `Plugin` type). Rather than risk that architecture
   boundary, `POST /sources` was extended to accept the same `schedule`/`weekday`/`hour`/`minute`
   fields directly and build the config **server-side**; a new `cli.Client.AddCronSource` method
   posts the raw fields. This still guarantees exactly one config-construction function
   (`BuildCronConfig`, called by the daemon either way) — the TUI just never links against it.
3. **No update/overwrite semantics for saved flows in this pass.** `SaveFlow` refuses if a flow of
   the same name already exists. A running or forkable definition's file being silently rewritten
   out from under it is a real correctness hazard (`L18-fork-replay-verify.md`'s DefinitionRef-
   must-stay-readable invariant), not just an inconvenience — this needed a real design decision
   (versioning? explicit `--force`? a new run tracing to a mutated file mid-flight?) that a small
   pass shouldn't rush. Named honestly as Future work below, not silently allowed.
4. **TUI's flow-create screen reads a local file path, not a multi-line text buffer.** No textarea
   widget is vendored in this codebase (bubbletea alone has no such component), and building one
   from scratch was out of proportion for this pass. A user typically authors YAML in a real editor
   anyway; the TUI screen asks for a path and reads it itself — the same "the TUI runs on the same
   machine, local file access is already this project's convention" logic `kairos flow create
   --file` already relies on.

## Real bugs found

None found in this pass's own new code — everything built and tested clean on the first pass. (One
pre-existing test-environment footgun was hit and self-diagnosed while manually verifying the new
CLI verbs: a stray `kairos serve` daemon from an earlier manual verification step was still bound
to the default port, causing a later manual test to fail with "daemon not reachable" — a leftover
process, not a code defect; noted here only for completeness, not as a shipped bug.)

## Tests

- `internal/registry/flow_test.go`: valid-save-then-Load-succeeds, invalid-save-rejected-with-no-
  file-left-behind, path-traversal-name-rejected, duplicate-name-rejected, list/get round-trips.
- `internal/api/flows_test.go`: the same claims through the real HTTP route — a saved flow really
  is `registry.Load`-clean afterward; a bad one is rejected with the real error text and no file.
- `internal/tasksource/cron_test.go`: `BuildCronConfig`'s exact JSON shape (including that `daily`
  omits `weekday` even if a stray value was passed), and its input validation.
- `internal/web/flows_test.go`: the editor's success/failure fragments (the composer-bugfix
  discipline extended to this new form), and that the cron form's fields produce byte-identical
  config to the CLI path.
- `internal/tui/flowsource_test.go`: real end-to-end smoke tests against a real daemon — a flow
  saved via the TUI is genuinely `kairos run`-able afterward; an invalid one shows the real error
  and leaves the create flow open for another attempt; a cron source created via the TUI's discrete
  fields is genuinely listable afterward with the right kind/config.
- `TestUI_everyCallHasCLICounterpart`/`TestUI_webRoutesResolve`: both stayed green with the three
  new Ops (`flow create`, `flow ls`, `flow run`) and their web mappings.

## Future work

- Saved-flow update/versioning (decision #3) — needs a real design for what happens to a run whose
  `DefinitionRef` points at a file that changes mid-run.
- Real `github`/`jira`/`linear` `TaskSource` implementations (NL-41, unchanged from L16) — until one
  exists, "friendly flags for github" would configure a source that silently never polls anything.
- Wiring the existing `Plugin` (NDJSON stdio) type into `Registry.Build` so a *custom* source kind
  becomes genuinely constructible — today `Registry.Build` only ever succeeds for `cron`/`inbox`/
  `fake`; `Plugin` exists and is tested but has no path from a `source` row's `kind` string to ever
  reaching it. This is a real, pre-existing gap this pass found while scoping friendly flags, not
  something this pass caused — flagged here rather than silently worked around, but left unbuilt:
  it needs `Registry.Build`'s constructor signature (or the `Manager`'s construction call site) to
  carry an `Executor`/`ScratchRoot`, a larger change than "add friendly flags to an existing kind."
- A directory-tree (not single-path) picker for `kairos flow create` on the web/TUI, matching the
  Projects page's picker — this pass used a plain path/textarea input for flows specifically, since
  a single file (not a directory) is being selected.
