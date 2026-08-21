package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newFSCmd backs `kairos fs browse [path]` — the CLI counterpart of the
// web UI's project-path picker (GET /fs/browse), kept real per this
// project's parity discipline even though a human at a real terminal
// would usually just type a path directly; `kairos -o json fs browse`
// still gives a scriptable directory listing.
func newFSCmd(app *appCtx) *cobra.Command {
	root := &cobra.Command{Use: "fs", Short: "filesystem helpers"}
	root.AddCommand(&cobra.Command{
		Use:   "browse [path]",
		Short: "list a directory's immediate real subdirectories (default: home)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			path := ""
			if len(args) == 1 {
				path = args[0]
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			resp, err := client.BrowseFS(ctx, path)
			if err != nil {
				return err
			}
			if app.output == "json" {
				return printJSON(cmd.OutOrStdout(), resp)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\n", resp.Path)
			for _, e := range resp.Entries {
				mark := ""
				if e.IsGit {
					mark = " (git)"
				}
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "  %s%s\n", e.Path, mark); err != nil {
					return err
				}
			}
			return nil
		},
	})
	return root
}
