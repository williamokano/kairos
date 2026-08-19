package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newDoctorCmd(app *appCtx) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "host preflight — what the daemon can and can't run",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			resp, err := client.Doctor(ctx)
			if err != nil {
				return err
			}
			if app.output == "json" {
				return printJSON(cmd.OutOrStdout(), resp)
			}
			w := cmd.OutOrStdout()
			tw := newTable(w)
			for _, c := range resp.Checks {
				mark := "✓"
				if !c.OK {
					mark = "✗"
				}
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", mark, c.Name, c.Detail)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			for _, d := range resp.Deferred {
				_, _ = fmt.Fprintf(w, "  (not yet checked: %s)\n", d)
			}
			return nil
		},
	}
}
