package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newShowCmd(app *appCtx) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "show a run's folded state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			state, err := client.GetRun(ctx, args[0])
			if err != nil {
				return err
			}
			if app.output == "json" {
				return printJSON(cmd.OutOrStdout(), state)
			}
			w := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(w, "run:    %s\n", state.ID)
			_, _ = fmt.Fprintf(w, "status: %s\n", state.Status)
			if len(state.Executions) > 0 {
				_, _ = fmt.Fprintln(w, "nodes:")
				tw := newTable(w)
				_, _ = fmt.Fprintln(tw, "  NODE\tEXEC ID\tSTATUS\tATTEMPT\tITERATION")
				for nodeID, execs := range state.Executions {
					for _, e := range execs {
						_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\t%d\t%d\n", nodeID, e.ExecID, e.Status, e.Attempt, e.Iteration)
					}
				}
				return tw.Flush()
			}
			return nil
		},
	}
	return cmd
}
