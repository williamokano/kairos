package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/williamokano/kairos/internal/cli"
)

// sseEventMsg is one live envelope pushed from the background SSE
// subscription into bubbletea's Update loop — replacing the 2-second
// tea.Tick poll (doc.go's former Documented decision #1) with a real
// push: the model refetches the moment an event lands, not on the next
// timer tick.
type sseEventMsg struct {
	env cli.Envelope
}

// sseStatusMsg reports a connection-lifecycle transition (connecting,
// connected, disconnected-and-retrying) from Client.FollowEvents, surfaced
// on the status line rather than silently swallowed.
type sseStatusMsg struct {
	status cli.FollowStatus
}

// sseSubscription bridges one long-lived background goroutine — reading
// GET /events over the daemon's unix socket via Client.FollowEvents — into
// bubbletea's synchronous, message-passing Update loop. This is the exact
// pattern bubbletea's own docs use for an external event source (e.g. its
// "realtime" example: a buffered channel the program reads one message at
// a time via a tea.Cmd that blocks on a channel receive), applied here to
// a real SSE subscription instead of a synthetic ticker. The goroutine
// never calls into bubbletea directly and never touches Model — it only
// ever writes to ch, so there is no shared mutable state to race on
// (verified under -race by TestSSESubscription_pushesEventsAsTheyLand).
type sseSubscription struct {
	ch chan tea.Msg
}

// newSSESubscription starts the background FollowEvents goroutine
// immediately, unless client is nil — TestScreens_renderWithoutPanicking
// and other unit tests construct a Model with client=nil and must never
// start a goroutine that will dereference it. ctx's cancellation is what
// stops the goroutine; it is the same context Model carries everywhere
// else (bound to the TUI program's lifetime, cancelled on detach/quit).
func newSSESubscription(ctx context.Context, client *cli.Client) *sseSubscription {
	sub := &sseSubscription{ch: make(chan tea.Msg, 64)}
	if client != nil {
		go sub.run(ctx, client)
	}
	return sub
}

// run subscribes to every stream (streamID="") from the beginning
// (afterSeq=0) and forwards each envelope and status transition onto ch.
// FollowEvents itself owns reconnect-with-backoff and Last-Event-ID-style
// resumption (internal/cli.Client.FollowEvents's doc comment); this
// function's only job is turning that callback API into channel sends a
// tea.Cmd can block on.
func (s *sseSubscription) run(ctx context.Context, client *cli.Client) {
	_ = client.FollowEvents(ctx, "", 0,
		func(env cli.Envelope) {
			select {
			case s.ch <- sseEventMsg{env: env}:
			case <-ctx.Done():
			}
		},
		func(status cli.FollowStatus) {
			select {
			case s.ch <- sseStatusMsg{status: status}:
			case <-ctx.Done():
			}
		},
	)
}

// waitForSSE turns "receive the next message from the subscription" into
// a tea.Cmd. Update re-issues it after every message so the model is
// always listening — the same one-cmd-reads-one-message, then-reissue
// chain bubbletea's docs use for channel-fed subscriptions.
func waitForSSE(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return <-ch
	}
}
