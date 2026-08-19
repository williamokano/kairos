package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newLsCmd(app *appCtx) *cobra.Command {
	var status string
	var quiet bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "list runs",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			runs, err := client.ListRuns(ctx, status)
			if err != nil {
				return err
			}
			if app.output == "json" {
				return printJSON(cmd.OutOrStdout(), runs)
			}
			if quiet {
				for _, r := range runs {
					if _, err := fmt.Fprintln(cmd.OutOrStdout(), r.RunID); err != nil {
						return err
					}
				}
				return nil
			}
			tw := newTable(cmd.OutOrStdout())
			_, _ = fmt.Fprintln(tw, "RUN ID\tSTATUS\tSTARTED\tUPDATED")
			for _, r := range runs {
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.RunID, r.Status, r.StartedAt, r.UpdatedAt)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "filter by run status")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "ids only, one per line")
	return cmd
}
