package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newProjectCmd backs `kairos project create/ls` — a named binding to a
// real working directory (internal/project), git-backed or not
// (auto-detected server-side).
func newProjectCmd(app *appCtx) *cobra.Command {
	root := &cobra.Command{Use: "project", Short: "manage Projects — named working directories for sessions"}

	var path string
	create := &cobra.Command{
		Use:   "create <name>",
		Short: "register a Project at a real path",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if path == "" {
				return &usageError{msg: "--path is required"}
			}
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			p, err := client.CreateProject(ctx, args[0], path)
			if err != nil {
				return err
			}
			if app.output == "json" {
				return printJSON(cmd.OutOrStdout(), p)
			}
			gitTag := "no-git"
			if p.GitBacked {
				gitTag = "git"
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", p.ID, p.Name, p.RepoPath, gitTag)
			return err
		},
	}
	create.Flags().StringVar(&path, "path", "", "the real directory this Project binds to (required)")
	root.AddCommand(create)

	root.AddCommand(&cobra.Command{
		Use:   "ls",
		Short: "list Projects",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			projects, err := client.ListProjects(ctx)
			if err != nil {
				return err
			}
			if app.output == "json" {
				return printJSON(cmd.OutOrStdout(), projects)
			}
			tw := newTable(cmd.OutOrStdout())
			_, _ = fmt.Fprintln(tw, "ID\tNAME\tPATH\tGIT")
			for _, p := range projects {
				gitTag := "no"
				if p.GitBacked {
					gitTag = "yes"
				}
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.ID, p.Name, p.RepoPath, gitTag)
			}
			return tw.Flush()
		},
	})

	return root
}
