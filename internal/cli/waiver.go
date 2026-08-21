package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newWaiverCmd is 05-gates.md's "waiver.grant is deny-tier for every
// non-human principal" made reachable for the human operator it's FOR —
// engine.GrantWaiver already existed and enforced this
// (L11-policy-secrets.md's own Future work named the missing CLI verb).
// --ttl is required and there is no --forever: an unexpiring waiver is
// exactly the silent, permanent bypass a waivable:false gate is designed
// to make impossible, so this command refuses to offer one even for a
// waivable:true gate. No --yes/--all here either, matching kairos
// approve's discipline exactly.
func newWaiverCmd(app *appCtx) *cobra.Command {
	root := &cobra.Command{
		Use:   "waiver",
		Short: "grant a time-limited waiver for a gate failure",
	}
	root.AddCommand(newWaiverGrantCmd(app))
	return root
}

func newWaiverGrantCmd(app *appCtx) *cobra.Command {
	var nodeID, gateID, reason, ttl string
	cmd := &cobra.Command{
		Use:   "grant <runID>",
		Short: "grant a waiver for one node's gate failure — never a rubber stamp",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if nodeID == "" {
				return fmt.Errorf("--node is required")
			}
			if gateID == "" {
				return fmt.Errorf("--gate is required")
			}
			if reason == "" {
				return fmt.Errorf("--reason is required")
			}
			if ttl == "" {
				return fmt.Errorf("--ttl is required — an unexpiring waiver is not offered")
			}
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			if err := client.GrantWaiver(ctx, args[0], nodeID, gateID, reason, ttl); err != nil {
				return err
			}
			if app.output == "json" {
				return printJSON(cmd.OutOrStdout(), map[string]string{"status": "granted"})
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "granted")
			return err
		},
	}
	cmd.Flags().StringVar(&nodeID, "node", "", "the node id the gate ran against (required)")
	cmd.Flags().StringVar(&gateID, "gate", "", "the gate id to waive (required)")
	cmd.Flags().StringVar(&reason, "reason", "", "why (required)")
	cmd.Flags().StringVar(&ttl, "ttl", "", "how long the waiver is valid, e.g. 24h (required)")
	return cmd
}
