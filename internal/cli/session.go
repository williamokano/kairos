package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newSessionCmd backs `kairos session start/ls` — a stable, resumable
// chat identity (internal/project.Session), elevating `kairos do
// --continue`'s run-chained continuation into something a user creates
// once and sends many `kairos do --session <id>` turns to.
func newSessionCmd(app *appCtx) *cobra.Command {
	root := &cobra.Command{Use: "session", Short: "manage chat Sessions — stable, resumable kairos do identities"}

	var project, actor string
	start := &cobra.Command{
		Use:   "start",
		Short: "start a new Session",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			s, err := client.StartSession(ctx, project, actor)
			if err != nil {
				return err
			}
			if app.output == "json" {
				return printJSON(cmd.OutOrStdout(), s)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", s.ID, s.Actor, s.WorkDir)
			return err
		},
	}
	start.Flags().StringVar(&project, "project", "", "an existing Project's name to bind this session to (git-backed projects get a real worktree)")
	start.Flags().StringVar(&actor, "actor", "", "actor kind (claude/codex/gemini/opencode/local); defaults to KAIROS_DEFAULT_DO_ACTOR")
	root.AddCommand(start)

	root.AddCommand(&cobra.Command{
		Use:   "ls",
		Short: "list Sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			sessions, err := client.ListSessions(ctx)
			if err != nil {
				return err
			}
			if app.output == "json" {
				return printJSON(cmd.OutOrStdout(), sessions)
			}
			tw := newTable(cmd.OutOrStdout())
			_, _ = fmt.Fprintln(tw, "ID\tACTOR\tPROJECT\tRUNS\tLAST USED\tBY")
			for _, s := range sessions {
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\n", s.ID, s.Actor, s.ProjectID, s.RunCount, s.LastUsedAt, s.CreatedBy)
			}
			return tw.Flush()
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "show <id>",
		Short: "show one Session's own record (including its ConversationRunID)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			s, err := client.GetSession(ctx, args[0])
			if err != nil {
				return err
			}
			if app.output == "json" {
				return printJSON(cmd.OutOrStdout(), s)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", s.ID, s.Actor, s.WorkDir, s.ConversationRunID)
			return err
		},
	})

	return root
}
