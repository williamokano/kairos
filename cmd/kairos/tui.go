package main

import (
	"context"

	"github.com/williamokano/kairos/internal/tui"
)

// runTUI is internal/cli's TUIFunc, wired here for the same reason
// serve.go's ServeFunc is: internal/tui needs internal/cli.Client to talk
// to the daemon, so internal/cli cannot import internal/tui back without
// creating an import cycle — cmd/kairos, the composition root, is where
// the two sides meet.
func runTUI(ctx context.Context, sockPath, homePath string) error {
	return tui.Run(ctx, sockPath, homePath)
}
