package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newDoCmd is `kairos do <text>` — 09-cli-and-tui.md's/L15-tui.md's
// named-but-unbuilt "start a run from prose" gap, closed here. Unlike
// `kairos run`, which needs a real workflow file already on disk, this
// synthesizes one from free text server-side (internal/api/do.go) — the
// same single code path (internal/tasksource.CreateRun) every other
// trigger source uses, so the run it creates is exactly as traceable and
// durable as any other.
func newDoCmd(app *appCtx) *cobra.Command {
	var continueRunID, sessionID string
	cmd := &cobra.Command{
		Use:   "do <text>",
		Short: "start (or continue) an ad hoc task from free text",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			resp, err := client.DoWithSession(ctx, args[0], continueRunID, sessionID)
			if err != nil {
				return err
			}
			if app.output == "json" {
				return printJSON(cmd.OutOrStdout(), resp)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", resp.RunID, resp.ConversationRunID)
			return err
		},
	}
	cmd.Flags().StringVar(&continueRunID, "continue", "", "continue an existing ad hoc conversation's run id, resuming its LLM session natively")
	cmd.Flags().StringVar(&sessionID, "session", "", "send this turn within a stable kairos session (see `kairos session start`); takes priority over --continue")
	return cmd
}
