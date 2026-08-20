package cli

import (
	"context"

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

func newServeCmd(app *appCtx) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "run the daemon in the foreground",
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.serve(cmd.Context())
		},
	}
}
