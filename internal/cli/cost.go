package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newCostCmd is the first client-facing surface for L07's admission
// daily-spend cap tracker — before this verb, the running total existed
// only inside the daemon's own process/database, never read back by a
// human. It is honest about what it is not: an estimate checked against a
// node's declared cost ceiling at admission time, never a reconciliation
// against real spend, which this codebase never learns (NL-30,
// 11-limitations.md — "cost accounting is always unknown for llm-actor
// nodes").
func newCostCmd(app *appCtx) *cobra.Command {
	return &cobra.Command{
		Use:   "cost",
		Short: "today's admission daily-spend estimate vs the configured cap",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()

			resp, err := client.Cost(ctx)
			if err != nil {
				return err
			}
			if app.output == "json" {
				return printJSON(cmd.OutOrStdout(), resp)
			}
			w := cmd.OutOrStdout()
			switch {
			case !resp.SpendRecorded:
				_, _ = fmt.Fprintf(w, "%s: no admitted request has recorded a cost estimate yet (cap $%.2f)\n", resp.Day, resp.DailyCapUSD)
			case resp.DailyCapUSD > 0:
				_, _ = fmt.Fprintf(w, "%s: $%.2f of $%.2f estimated cap spent\n", resp.Day, resp.SpentUSD, resp.DailyCapUSD)
			default:
				_, _ = fmt.Fprintf(w, "%s: $%.2f estimated spend (no cap configured)\n", resp.Day, resp.SpentUSD)
			}
			_, _ = fmt.Fprintln(w, "note: this is an admission-time ESTIMATE, never reconciled against real spend (NL-30)")
			return nil
		},
	}
}
