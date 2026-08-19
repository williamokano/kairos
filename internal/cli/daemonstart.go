package cli

import (
	"context"
	"fmt"
	"time"
)

// DaemonStarter spawns the daemon process. Only cmd/kairos/main.go
// constructs a real implementation (exec.Command(self, "serve") with
// Setsid, so the daemon survives the invoking terminal closing) — that is
// the one narrow, documented exemption to "internal/cli never imports
// os/exec", justified because starting the daemon is the binary
// bootstrapping its own second role, not a workflow actor process
// (L04-daemon-api-cli.md's decision #4). Tests inject a fake.
type DaemonStarter interface {
	Start(ctx context.Context) error
}

// ensureDaemon pings the socket; if nothing answers, it asks starter to
// spawn a daemon and polls the socket for up to timeout before giving up.
// "kairos <verb> ... starts a daemon if none is running" (01-architecture.md)
// is core behaviour, not deferred.
func ensureDaemon(ctx context.Context, client *Client, starter DaemonStarter, timeout time.Duration) error {
	if client.Ping(ctx) {
		return nil
	}
	if err := starter.Start(ctx); err != nil {
		return fmt.Errorf("starting daemon: %w", err)
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if client.Ping(ctx) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return fmt.Errorf("daemon did not become ready within %s", timeout)
}
