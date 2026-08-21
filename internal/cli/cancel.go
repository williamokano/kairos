package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newCancelCmd is `kairos cancel <runID>` (09-cli-and-tui.md). Unlike
// `kairos fork`, cancellation always compensates applied effects — the
// engine's shard-level handler (internal/engine/shard.go) runs
// compensateRun unconditionally on the Running/Degraded -> Cancelled
// transition; there is no --compensate flag to thread through because
// there is nothing for it to toggle (see internal/engine/cancel.go's doc
// comment). A --reason flag is required, matching `kairos approve`'s own
// "no silent destructive action" discipline (every human decision records
// why).
func newCancelCmd(app *appCtx) *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "cancel <runID>",
		Short: "cancel a run — every applied effect is compensated automatically",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if reason == "" {
				return &usageError{msg: "--reason is required"}
			}
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			if err := client.Cancel(ctx, args[0], reason); err != nil {
				return err
			}
			if app.output == "json" {
				return printJSON(cmd.OutOrStdout(), map[string]string{"status": "cancelled"})
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "cancelled")
			return err
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "why this run is being cancelled (required)")
	return cmd
}
