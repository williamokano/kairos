package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// daemonStarter is the real DaemonStarter (internal/cli/daemonstart.go):
// it re-executes this same binary as `kairos serve`, detached from the
// invoking terminal (Setsid) so closing the terminal does not kill the
// daemon — matching "Ctrl-C detaches; it does not kill" (09-cli-and-tui.md).
// Living here, not in internal/cli, is decision #4: starting the daemon
// is the binary's own bootstrap, not a workflow actor process.
type daemonStarter struct{}

func (daemonStarter) Start(ctx context.Context) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving own executable path: %w", err)
	}
	cmd := exec.Command(self, "serve")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting daemon: %w", err)
	}
	return nil
}
