package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

// ServeFunc runs the daemon boot sequence until ctx is cancelled. The real
// implementation lives in cmd/kairos (it needs internal/api, which no
// other package may import — dependencyDirection's "nothing imports
// internal/api" rule — and os/exec for the toolchain-presence doctor
// checks, decision #4/#6's exemption). internal/cli only knows the shape.
type ServeFunc func(ctx context.Context) error

// TUIFunc runs the full-screen TUI against the daemon's socket until the
// user detaches or quits. The real implementation lives in cmd/kairos
// (internal/tui, which it wires here, is otherwise unaware of internal/cli
// — internal/tui imports internal/cli.Client to talk to the daemon, so
// internal/cli itself cannot import internal/tui back without an import
// cycle; this injection is that boundary, the same shape as ServeFunc
// above).
type TUIFunc func(ctx context.Context, sockPath, homePath string) error

// WebFunc ensures the daemon (which already serves the web UI, per
// 10-webui.md — `kairos serve` binds it alongside the admin socket) is
// running, then opens a browser at a one-time-token URL. Injected from
// cmd/kairos for the same reason as ServeFunc/TUIFunc: opening a browser
// needs os/exec, which internal/cli may not import.
type WebFunc func(ctx context.Context, sockPath, homePath string) error

func newServeCmd(app *appCtx) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "run the daemon in the foreground",
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.serve(cmd.Context())
		},
	}
}

func newWebCmd(app *appCtx) *cobra.Command {
	return &cobra.Command{
		Use:   "web",
		Short: "open the web UI in a browser",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := ensureClient(cmd, app); err != nil {
				return err
			}
			if app.web == nil {
				return fmt.Errorf("web UI support not wired into this binary")
			}
			return app.web(cmd.Context(), app.sockPath, app.homePath)
		},
	}
}
