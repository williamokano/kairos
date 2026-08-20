package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newForkCmd is `kairos fork` — 06-durability.md: "copy events 1..N into
// a new run stream tagged forkedFrom, restore the workspace to the same
// point, append run.forked." Refuses by default if no exact workspace
// snapshot exists at --at (ErrWorkspaceDrift, surfaced as a 409); --allow-
// drift is required, spelled out, to proceed anyway — matching
// kairos approve's no-silent-bypass discipline.
func newForkCmd(app *appCtx) *cobra.Command {
	var atSequence int
	var overridesFlag []string
	var allowDrift bool
	cmd := &cobra.Command{
		Use:   "fork <runID>",
		Short: "fork a run at a recorded sequence — copies reasoning exactly, restores the workspace approximately",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			overrides := map[string]string{}
			for _, kv := range overridesFlag {
				k, v, ok := splitKV(kv)
				if !ok {
					return &usageError{msg: "invalid --set, want k=v: " + kv}
				}
				overrides[k] = v
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			result, err := client.Fork(ctx, args[0], atSequence, overrides, allowDrift)
			if err != nil {
				return err
			}
			if app.output == "json" {
				return printJSON(cmd.OutOrStdout(), result)
			}
			w := cmd.OutOrStdout()
			if _, err := fmt.Fprintf(w, "%s\n", result.NewRunID); err != nil {
				return err
			}
			if result.Drifted {
				_, err = fmt.Fprintf(w, "  (workspace drifted: restored from sequence %d instead)\n", result.ActualSnapshotSeq)
			}
			return err
		},
	}
	cmd.Flags().IntVar(&atSequence, "at", 0, "the sequence to fork at (default: the latest snapshot, or the run's latest event)")
	cmd.Flags().StringArrayVar(&overridesFlag, "set", nil, "param override, k=v (repeatable) — e.g. --set actor.implement=sonnet")
	cmd.Flags().BoolVar(&allowDrift, "allow-drift", false, "proceed even without an exact workspace snapshot at --at, recording fork.workspace.drifted")
	return cmd
}
