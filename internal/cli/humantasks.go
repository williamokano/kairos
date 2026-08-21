package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newHumanTasksCmd is L20-webui.md's Documented decision #5's real,
// indexed "what's currently waiting on you" verb — queue only, never
// answers (that's `kairos approve`'s job), matching 09-cli-and-tui.md's
// Inbox screen framing exactly.
func newHumanTasksCmd(app *appCtx) *cobra.Command {
	return &cobra.Command{
		Use:   "human-tasks",
		Short: "list what's currently waiting on you",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			tasks, err := client.ListOpenHumanTasks(ctx)
			if err != nil {
				return err
			}
			if app.output == "json" {
				return printJSON(cmd.OutOrStdout(), tasks)
			}
			w := cmd.OutOrStdout()
			tw := newTable(w)
			_, _ = fmt.Fprintln(tw, "RUN ID\tNODE\tKIND\tOPENED AT")
			for _, t := range tasks {
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", t.RunID, t.NodeID, t.Kind, t.OpenedAt)
			}
			return tw.Flush()
		},
	}
}
