package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newStatusCmd(app *appCtx) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "running? active runs, uptime",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(cmd, app)
		},
	}
}

// runStatus backs both `kairos status` and bare `kairos` (no TUI exists
// yet — L15 changes bare `kairos` to attach one instead, when stdout is a
// TTY).
func runStatus(cmd *cobra.Command, app *appCtx) error {
	client, err := ensureClient(cmd, app)
	if err != nil {
		return err
	}
	ctx, cancel := withTimeout(cmd)
	defer cancel()
	resp, err := client.Status(ctx)
	if err != nil {
		return err
	}
	if app.output == "json" {
		return printJSON(cmd.OutOrStdout(), resp)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "daemon pid %d, uptime %s, %d active run(s)\n", resp.DaemonPID, resp.Uptime, resp.ActiveRuns)
	return err
}
