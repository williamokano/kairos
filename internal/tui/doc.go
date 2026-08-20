// Package tui is Kairos's terminal UI — a bubbletea program that is a pure
// client of the daemon's existing HTTP API over the unix socket
// (internal/cli.Client), exactly like the CLI itself. It never imports
// os/exec or any internal/executor/* package (enforced by
// internal/archtest's TestArchitecture_tuiHasNoExecution) and never talks
// to internal/engine, internal/eventstore, or internal/domain directly —
// see adr/0008-terminal-is-a-client.md for why: a renderer's lifetime is a
// terminal session, and the work this system does must outlive it.
//
// Live updates are polling-based for now (a periodic re-fetch on a tea.Tick,
// not a persistent SSE-push binding into the bubbletea update loop) — see
// L15-tui.md's Documented decisions for why that's a real, honest scope cut
// rather than the doc's fuller "SSE, resumable by Last-Event-ID" design.
package tui
