# L15 — The TUI

## Depends on

L14 (conversations) and L18 (fork + replay verify), both committed. This document also completes
L04's deferred one-line change to bare `kairos`.

## Scope

**In.**
- `internal/tui`: a `bubbletea` program that is a pure client of the daemon's existing HTTP API
  over the unix socket (`internal/cli.Client`) — never `os/exec`, never `internal/executor/*`,
  never `internal/engine`/`internal/eventstore`/`internal/domain` directly.
- Seven screens: Home, Conversation, Run inspector, Log follow, Inbox, Runners, Benchmark.
- The decision/approval screen and every anti-rubber-stamp mechanic `09-cli-and-tui.md` specifies:
  fixed pane order, computed (never model-authored) risk, focus-order enforcement, the typed
  decision word, no bulk/global approve, evidence-load-failure blocking the form, a separate
  risk-acceptance control for high/critical findings.
- The 80×24 refusal, specifically and only on the decision screen.
- The command palette (`:`), resolving a run id, a screen name, or a verb — no fuzzy search, no
  history.
- The two-mode (NAV/INPUT) keyboard model and the keybindings table.
- Bare `kairos`: attaches the TUI when stdout is a TTY, prints `status` and exits 0 otherwise.
- `TestArchitecture_tuiHasNoExecution` made real (it already existed as a passing-vacuously stub
  since before this document, per the same pattern L08's `TestArchitecture_agentSocketRouteSubset`
  used).

**Out.** The web UI (L20); real runner add/probe/drain management (07-runners.md, a later
document — this document renders the one real `local` runner honestly); `kairos do` and any other
verb needing a daemon-side endpoint that doesn't exist yet; log streaming (no HTTP route serves
stdout/stderr content yet); desktop/terminal notifications, `OSC 2`, shell-prompt integration,
`BEL` (09-cli-and-tui.md's "Getting told" section — a distinct subsystem, not touched here); a
full N-way fork-and-vary benchmark flow (this document's Benchmark screen is a real two-run
`kairos compare`, reusing L18 directly, not a fork picker).

## Documented decisions

1. **Live updates are polling, not SSE push.** `09-cli-and-tui.md` specifies SSE over the unix
   socket, resumable by `Last-Event-ID`, as the live-update mechanism. Wiring a persistent SSE
   subscription into `bubbletea`'s `Update` loop (a background goroutine feeding `tea.Program.Send`)
   is a real, larger undertaking than this document's remaining budget allows cleanly; a 2-second
   `tea.Tick` re-fetch is the honest, working stand-in, named here rather than silently presented
   as the real thing. Future work.
2. **This codebase's IDs carry no `run_`/`ht_`/`nex_`/`src_` prefix** — every entity ID is a bare
   ULID (confirmed by reading `internal/domain`/`internal/eventstore`, which never adds one). The
   command palette's ULID-shape check (`looksLikeULID`) resolves any 26-character Crockford-shaped
   token to the run inspector rather than dispatching on a prefix that was never implemented.
3. **The decision screen's evidence is assembled by scanning the run's real event log**
   (`internal/cli.Client.Events`, a new client method, reused nowhere else yet), not from a
   summarized "decision context" endpoint — none exists. `constraint.evaluated` gives gate
   verdicts, `node.gates.evaluated` gives findings, `effect.confirmation.requested`/`.parked` give
   the effect and its reversibility. This is genuinely computed from durable facts, satisfying
   "risk is computed, never authored by a model" — just narrower than the mockup's richer fields
   (files outside the workspace, network egress list, session compaction count), none of which any
   existing event carries yet. Registered honestly rather than fabricated.
4. **`isIrreversibleEffect` is a two-entry allowlist** (`git.push`, `gh.pr.create` — the only two
   builtins `internal/effect`, L12, actually implements) rather than a general reversibility
   taxonomy, which does not exist in this codebase.
5. **The typed-confirm field's wire value is always the node ID**, matching
   `internal/engine/human.go`'s real `checkDecisionWeight` check (`TypedWord` must equal `nodeID`
   exactly, and only enforced server-side when `wait.weight: type`). The TUI's own anti-rubber-stamp
   rule is stricter than the wire contract on purpose — every decision requires typing the chosen
   word (`approve`/`request-changes`/`reject`) locally, in addition to sending the node-ID-shaped
   `TypedWord` the server actually checks — so the UI-level rule holds even on `silent`/`glance`/
   `read`-weight nodes the server itself doesn't gate.
6. **`Q` requires an explicit `y`/`n` line, not a double-press.** A hidden "press twice to confirm"
   is an easy accidental trigger; a visible prompt line the user must answer is the same anti-
   rubber-stamp posture the decision screen itself uses, applied to the one other destructive
   single-key action in the design.
7. **The Runners screen renders exactly one row** (`local`) sourced from real `GET /doctor` data,
   not a `kairos src ls`-shaped listing — sources (L16) and runners are different concepts, and
   conflating them to have "more data to show" would be dishonest. "A screen that shows one row is
   honest about there being one row" (09-cli-and-tui.md, verbatim).
8. **Onboarding's acknowledgement is a `$KAIROS_HOME/.onboarded` marker file, not a domain event**
   yet. `09-cli-and-tui.md` frames the acknowledgement as "a fact in the log, not a flag"; a real
   `onboarding.acknowledged` event plus daemon-side handling is more machinery than this document's
   remaining scope affords cleanly. Named as Future work, not silently faked as a flag either — the
   marker is real and persisted, just not yet a durable engine-side fact.
9. **`internal/tui` cannot import `internal/cli` back into `internal/cli` itself** — an import
   cycle, since `internal/tui` imports `internal/cli.Client`. Resolved with the exact shape L04's
   `ServeFunc` already established: a new `cli.TUIFunc` type, injected from `cmd/kairos` (the
   composition root, `cmd/kairos/tui.go`), mirroring `ServeFunc`'s doc comment precisely.
10. **`isTTY` is a plain `os.ModeCharDevice` stat check**, stdlib-only — no new dependency for
    terminal detection, even though `github.com/charmbracelet/x/term` is already pulled in
    transitively by `bubbletea`. The stdlib check is sufficient for "is a script piping me."

## Public interfaces

```go
// internal/tui
func New(ctx context.Context, client *cli.Client, homePath string, onboarded bool) Model
func Run(ctx context.Context, sockPath, homePath string) error // bubbletea program bootstrap

// internal/cli, additive
type TUIFunc func(ctx context.Context, sockPath, homePath string) error
func Execute(args []string, starter DaemonStarter, serve ServeFunc, tui TUIFunc) int // signature changed
func (c *Client) Events(ctx context.Context, streamID string, readTimeout time.Duration) ([]Envelope, error)
type Envelope struct { StreamID string; Sequence int; GlobalSeq int64; EventType string; Event json.RawMessage }
```

## Files to create

```
internal/tui/doc.go  model.go  fetch.go  keys.go  keys_decision.go  palette.go  entry.go
internal/tui/decision.go  screens_state.go
internal/tui/screens_home.go  screens_conversation.go  screens_runinspector.go  screens_logs.go
internal/tui/screens_inbox.go  screens_runners.go  screens_benchmark.go  screens_onboarding.go
internal/tui/view_decision.go
internal/tui/palette_test.go  decision_test.go  model_test.go

cmd/kairos/tui.go
cmd/kairos/tui_test.go

# modified:
internal/cli/root.go  serve.go  client.go
cmd/kairos/main.go
```

## Data changes

None. No new SQLite tables or event types — the decision screen reads existing events
(`constraint.evaluated`, `node.gates.evaluated`, `effect.confirmation.requested`/`.parked`)
rather than adding new ones.

## Acceptance criteria

- `go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run` clean; `go test ./... -race`
  green across every package, including `cmd/kairos`'s real-binary tests.
- All nine architecture tests pass, including the now-real `TestArchitecture_tuiHasNoExecution`
  (`internal/tui` imports neither `os/exec` nor any `internal/executor/*` package, and its
  violation fixture is caught).
- `TestUI_everyCallHasCLICounterpart` stays green — no new API route was added without a CLI
  counterpart (none was added; `Client.Events` is a client-side addition only).
- Every anti-rubber-stamp rule is a real, tested behavioral invariant (see Tests below) — not
  merely documented as intent.
- `make cross` builds `CGO_ENABLED=0` for darwin/linux × arm64/amd64, confirming `bubbletea`/
  `lipgloss` are genuinely cgo-free.
- `make arch` clean.
- No `TODO`, `FIXME`, or commented-out code in the diff.

## Tests

- `internal/tui/decision_test.go`: focus-order enforcement (the decision pane is unreachable until
  every prior pane has been viewed AND every high/critical finding's risk has been separately
  accepted), the typed-word requirement (exact match required, no single key ever approves),
  evidence-load-failure blocking the form (only retry/esc live), the 80×24 refusal.
- `internal/tui/model_test.go`: a rendering smoke test per screen (`Model.View()` against real-
  shaped fetched data, driven directly rather than through a full `tea.Program` against a live
  terminal — there is no TTY in a test binary to drive one against); a structural AST check that
  the inbox screen's source never calls `ApproveHumanTask` directly; a structural AST check that
  `ApproveHumanTask` has exactly one call site in the whole package (no undocumented bulk/shortcut
  approve path).
- `internal/tui/palette_test.go`: screen-name/verb/ULID resolution, no fuzzy matching, a structural
  reflection-based check that no field anywhere in `Model`'s type graph looks like a command-
  palette history feature.
- `cmd/kairos/tui_test.go`: `TestKairos_bareNonTTYPrintsStatusAndExits` — a real subprocess with
  piped (non-TTY) stdout against a real daemon, confirming it prints a status line and exits 0
  within 5s rather than attaching the full-screen TUI. The TTY-attaches branch itself needs a real
  pseudo-terminal to automate and is not covered here — see Future work.

## Benchmarks

None. Nothing here is on a durability-sensitive hot path.

## Migration

None from a prior version.

## Future work

- Real SSE-push live updates, replacing the 2-second poll (decision #1).
- A `TestKairos_bareTTYAttachesTheTUI` using a real pseudo-terminal (e.g. `github.com/creack/pty` —
  not yet an approved dependency, would need its own ADR) to automate the branch
  `TestKairos_bareNonTTYPrintsStatusAndExits` deliberately leaves uncovered.
- A `onboarding.acknowledged` domain event, replacing the `.onboarded` marker file (decision #8).
- A log-streaming API endpoint so the Log follow screen can render real content instead of pointing
  at the file on disk.
- Full runner management (add/probe/drain) once `07-runners.md`'s document exists, replacing the
  Runners screen's honest single-row placeholder.
- A full N-way fork-and-vary Benchmark flow (a run picker plus `Engine.Fork`, on top of this
  document's real two-run `kairos compare` slice).
- The "Getting told" notification layers (desktop notification, `OSC 2`, shell-prompt integration,
  `ntfy`) — an entirely separate subsystem `09-cli-and-tui.md` describes, untouched here.
- `kairos do` (start a run from prose) needs a daemon-side endpoint accepting free text before the
  Home screen's composer can do anything beyond showing an honest "not yet wired" message.
