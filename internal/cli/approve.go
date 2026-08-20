package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newApproveCmd is 09-cli-and-tui.md's "the CLI path exists but cannot
// rubber-stamp": --confirm and --reason exist and are both required;
// --yes, --all, and -f do not exist anywhere in this command, on purpose
// — see internal/archtest or approve_test.go's flag-introspection test
// for the check that keeps it that way.
func newApproveCmd(app *appCtx) *cobra.Command {
	var nodeID, decision, reason, typedWord string
	cmd := &cobra.Command{
		Use:   "approve <runID>",
		Short: "answer a Waiting(human) node's decision — never a rubber stamp",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if nodeID == "" {
				return fmt.Errorf("--node is required")
			}
			if decision == "" {
				return fmt.Errorf("--confirm is required")
			}
			if reason == "" {
				return fmt.Errorf("--reason is required for every decision, not just negative ones")
			}
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			if err := client.ApproveHumanTask(ctx, args[0], nodeID, decision, reason, typedWord); err != nil {
				return err
			}
			if app.output == "json" {
				return printJSON(cmd.OutOrStdout(), map[string]string{"status": "answered"})
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "answered")
			return err
		},
	}
	cmd.Flags().StringVar(&nodeID, "node", "", "the wait: human node id to answer (required)")
	cmd.Flags().StringVar(&decision, "confirm", "", "the decision, typed out in full — e.g. approve, reject (required)")
	cmd.Flags().StringVar(&reason, "reason", "", "why (required for every decision)")
	cmd.Flags().StringVar(&typedWord, "typed-confirm", "", "the node id, typed out — required only when the node's wait.weight is \"type\"")
	return cmd
}
