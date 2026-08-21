package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newDiffCmd is `kairos diff <run> [node]` (09-cli-and-tui.md: "the change
// it produced") — the CLI counterpart of the web diff viewer
// (L20-webui.md's Future work, GET /runs/{id}/diff). With no node, it is
// the whole run's change against the project's configured base ref; with
// one, it is that node's own before/after boundary, plus a
// scope-violations line when the node declares workspacePaths.
func newDiffCmd(app *appCtx) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "diff <run> [node]",
		Short: "the file change a run (or one of its nodes) produced",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()

			nodeID := ""
			if len(args) == 2 {
				nodeID = args[1]
			}
			result, err := client.Diff(ctx, args[0], nodeID)
			if err != nil {
				return err
			}
			if app.output == "json" {
				return printJSON(cmd.OutOrStdout(), result)
			}

			w := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(w, "%s..%s\n", result.FromRef, result.ToRef)
			for _, f := range result.Files {
				if f.Binary {
					_, _ = fmt.Fprintf(w, "  %s (binary)\n", f.Path)
					continue
				}
				_, _ = fmt.Fprintf(w, "  %s +%d -%d\n", f.Path, f.Added, f.Removed)
			}
			if len(result.ScopeViolations) > 0 {
				_, _ = fmt.Fprintf(w, "scope violations (outside %v): %v\n", result.WorkspacePaths, result.ScopeViolations)
			}
			_, _ = fmt.Fprintln(w, "---")
			_, _ = fmt.Fprint(w, result.Patch)
			return nil
		},
	}
	return cmd
}
