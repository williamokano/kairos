package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newDBCmd(app *appCtx) *cobra.Command {
	root := &cobra.Command{
		Use:   "db",
		Short: "event store maintenance",
	}
	root.AddCommand(&cobra.Command{
		Use:   "verify",
		Short: "replay every stream and diff it against the persisted projections",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			resp, err := client.DBVerify(ctx)
			if err != nil {
				return err
			}
			if app.output == "json" {
				return printJSON(cmd.OutOrStdout(), resp)
			}
			if len(resp.MismatchedRunIDs) == 0 {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "clean")
				return err
			}
			for _, id := range resp.MismatchedRunIDs {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "mismatch:", id)
			}
			return &invariantViolationError{msg: fmt.Sprintf("%d run(s) mismatched", len(resp.MismatchedRunIDs))}
		},
	})
	root.AddCommand(&cobra.Command{
		Use:   "reindex",
		Short: "force every projection to rebuild",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			if err := client.DBRebuild(ctx); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "ok")
			return err
		},
	})
	return root
}

type invariantViolationError struct{ msg string }

func (e *invariantViolationError) Error() string { return e.msg }
