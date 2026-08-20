package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newConversationCmd(app *appCtx) *cobra.Command {
	root := &cobra.Command{
		Use:   "conversation",
		Short: "a run's Conversation — the message thread wait: conversation nodes suspend on",
	}
	root.AddCommand(&cobra.Command{
		Use:   "show <runID>",
		Short: "show a run's conversation messages",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			msgs, err := client.GetConversation(ctx, args[0])
			if err != nil {
				return err
			}
			if app.output == "json" {
				return printJSON(cmd.OutOrStdout(), msgs)
			}
			w := cmd.OutOrStdout()
			for _, m := range msgs {
				_, _ = fmt.Fprintf(w, "%s: %s\n", m.Role, m.Text)
			}
			return nil
		},
	})
	root.AddCommand(&cobra.Command{
		Use:   "send <runID> <text>",
		Short: "post a message to a run's conversation — resolves a wait: conversation node",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			if err := client.PostConversationMessage(ctx, args[0], args[1]); err != nil {
				return err
			}
			if app.output == "json" {
				return printJSON(cmd.OutOrStdout(), map[string]string{"status": "sent"})
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "sent")
			return err
		},
	})
	return root
}
