package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// newCompareCmd is `kairos compare` — 12-build-plan.md: "kairos compare
// shows cost, duration, attempts, and findings side by side." Cost is
// omitted (see internal/api/compare.go's doc comment: never metered,
// reporting one would be fabricating it); drift is annotated when one
// side is a fork of the other with a recorded fork.workspace.drifted.
func newCompareCmd(app *appCtx) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compare <runA> <runB>",
		Short: "compare two runs side by side — duration, attempts, findings, fork drift",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			result, err := client.Compare(ctx, args[0], args[1])
			if err != nil {
				return err
			}
			if app.output == "json" {
				return printJSON(cmd.OutOrStdout(), result)
			}
			w := cmd.OutOrStdout()
			tw := newTable(w)
			_, _ = fmt.Fprintln(tw, "\tA\tB")
			_, _ = fmt.Fprintf(tw, "run\t%s\t%s\n", result.A.RunID, result.B.RunID)
			_, _ = fmt.Fprintf(tw, "status\t%s\t%s\n", result.A.Status, result.B.Status)
			_, _ = fmt.Fprintf(tw, "duration\t%s\t%s\n", time.Duration(result.A.Duration), time.Duration(result.B.Duration))
			_, _ = fmt.Fprintf(tw, "attempts\t%d\t%d\n", result.A.Attempts, result.B.Attempts)
			_, _ = fmt.Fprintf(tw, "findings\t%d\t%d\n", result.A.Findings, result.B.Findings)
			if err := tw.Flush(); err != nil {
				return err
			}
			if result.A.Drifted {
				_, _ = fmt.Fprintf(w, "  (A: %s)\n", result.A.DriftDetail)
			}
			if result.B.Drifted {
				_, _ = fmt.Fprintf(w, "  (B: %s)\n", result.B.DriftDetail)
			}
			return nil
		},
	}
	return cmd
}
