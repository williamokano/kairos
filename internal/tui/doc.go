// Package tui is Kairos's terminal UI — a bubbletea program that is a pure
// client of the daemon's existing HTTP API over the unix socket
// (internal/cli.Client), exactly like the CLI itself. It never imports
// os/exec or any internal/executor/* package (enforced by
// internal/archtest's TestArchitecture_tuiHasNoExecution) and never talks
// to internal/engine, internal/eventstore, or internal/domain directly —
// see adr/0008-terminal-is-a-client.md for why: a renderer's lifetime is a
// terminal session, and the work this system does must outlive it.
//
// Live updates are a real, persistent SSE subscription (sse.go), not a
// tea.Tick poll: one background goroutine holds GET /events open via
// internal/cli.Client.FollowEvents, resuming by GlobalSeq (the same
// contract as Last-Event-ID, ADR 0010) across reconnects, and bridges
// envelopes into bubbletea's Update loop the way bubbletea's own docs
// describe for a channel-fed external event source — see L15-tui.md's
// Documented decisions and L22-harness-integration.md's SSE-live-clients
// section for the history (this replaced an earlier 2-second-poll stand-in
// named honestly as such at the time).
package tui
