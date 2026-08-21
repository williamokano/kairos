package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newEffectsCmd is 05-gates.md's "what has been applied and what
// compensation would unwind" surface, plus the manual escape hatch for a
// node blocked in effect.unknown (L12-effects-compensation.md's own
// Future work — both were previously daemon-side data/logic with no CLI
// verb reaching them).
func newEffectsCmd(app *appCtx) *cobra.Command {
	root := &cobra.Command{
		Use:   "effects <runID>",
		Short: "list a run's recorded effect actions",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			summaries, err := client.Effects(ctx, args[0])
			if err != nil {
				return err
			}
			if app.output == "json" {
				return printJSON(cmd.OutOrStdout(), summaries)
			}
			w := cmd.OutOrStdout()
			tw := newTable(w)
			_, _ = fmt.Fprintln(tw, "NODE\tEFFECT\tOUTCOME\tEXTERNAL REF\tCOMPENSATED\tWOULD COMPENSATE ON CANCEL")
			for _, s := range summaries {
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%t\t%t\n",
					s.NodeID, s.Effect, s.Outcome, s.ExternalRef, s.Compensated, s.WouldCompensateOnCancel)
			}
			return tw.Flush()
		},
	}
	root.AddCommand(newEffectsResolveCmd(app))
	return root
}

// newEffectsResolveCmd is deliberately as strict as `kairos approve`:
// --outcome and --reason are both required, and there is no `--yes`/
// `--all` — resolving a stuck external mutation without direct
// event-store access is exactly the kind of decision this codebase never
// lets be rubber-stamped (see approve.go's identical discipline).
func newEffectsResolveCmd(app *appCtx) *cobra.Command {
	var nodeID, outcome, reason string
	cmd := &cobra.Command{
		Use:   "resolve <runID>",
		Short: "manually resolve a node blocked in effect.unknown — never a rubber stamp",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if nodeID == "" {
				return fmt.Errorf("--node is required")
			}
			if outcome != "applied" && outcome != "failed" {
				return fmt.Errorf("--outcome must be \"applied\" or \"failed\", got %q", outcome)
			}
			if reason == "" {
				return fmt.Errorf("--reason is required")
			}
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			if err := client.ResolveEffect(ctx, args[0], nodeID, outcome, reason); err != nil {
				return err
			}
			if app.output == "json" {
				return printJSON(cmd.OutOrStdout(), map[string]string{"status": "resolved"})
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "resolved")
			return err
		},
	}
	cmd.Flags().StringVar(&nodeID, "node", "", "the node id blocked in effect.unknown (required)")
	cmd.Flags().StringVar(&outcome, "outcome", "", "applied | failed (required)")
	cmd.Flags().StringVar(&reason, "reason", "", "why you know this outcome to be true (required)")
	return cmd
}
