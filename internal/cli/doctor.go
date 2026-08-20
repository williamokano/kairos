package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newDoctorCmd's --self-check flag is L19's on-demand health check —
// db verify plus a live orphan-process/orphan-workspace scan
// (internal/engine.SelfCheck) — layered onto the same verb rather than a
// separate "kairos selfcheck" top-level command, since `doctor` is
// already "the host probe" this deepens rather than duplicates.
func newDoctorCmd(app *appCtx) *cobra.Command {
	var selfCheck bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "host preflight — what the daemon can and can't run",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()

			if selfCheck {
				report, err := client.SelfCheck(ctx)
				if err != nil {
					return err
				}
				if app.output == "json" {
					return printJSON(cmd.OutOrStdout(), report)
				}
				w := cmd.OutOrStdout()
				if report.DBClean {
					_, _ = fmt.Fprintln(w, "db: clean")
				} else {
					_, _ = fmt.Fprintf(w, "db: %d run(s) mismatched: %v\n", len(report.MismatchedRunIDs), report.MismatchedRunIDs)
				}
				if len(report.UnverifiableExecutions) == 0 {
					_, _ = fmt.Fprintln(w, "processes: clean")
				} else {
					_, _ = fmt.Fprintf(w, "processes: %d unverifiable execution(s): %v\n", len(report.UnverifiableExecutions), report.UnverifiableExecutions)
				}
				_, _ = fmt.Fprintf(w, "workspaces: %d orphan(s) removed: %v\n", len(report.OrphanWorkspacesRemoved), report.OrphanWorkspacesRemoved)
				if !report.DBClean || len(report.UnverifiableExecutions) > 0 {
					return &invariantViolationError{msg: "self-check found problems"}
				}
				return nil
			}

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
	cmd.Flags().BoolVar(&selfCheck, "self-check", false, "run a live health check: event-log integrity, orphan processes, orphan workspaces")
	return cmd
}
