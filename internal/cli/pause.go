package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

func newPauseCmd(app *appCtx) *cobra.Command {
	return &cobra.Command{
		Use:   "pause",
		Short: "stop admitting new node executions; nodes already running finish naturally",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			if err := client.Pause(ctx); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "paused")
			return err
		},
	}
}

func newResumeCmd(app *appCtx) *cobra.Command {
	return &cobra.Command{
		Use:   "resume",
		Short: "resume admitting new node executions after pause/park",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			if err := client.Resume(ctx); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "resumed")
			return err
		},
	}
}

// newParkCmd is 09-cli-and-tui.md's "closing the lid": pause, and with
// --wait, poll until every run has genuinely drained to its next node
// boundary (InFlight reaches 0) before returning — so a caller scripting
// `kairos park --wait && shutdown` knows nothing is mid-write when it
// runs. Stopping the daemon process itself is a separate concern (`kairos
// up`/`down` lifecycle verbs, named in 09-cli-and-tui.md but not yet
// built — see L19-self-check-chaos.md's Future work); park only pauses
// dispatch and waits, it does not stop the daemon.
func newParkCmd(app *appCtx) *cobra.Command {
	var wait bool
	cmd := &cobra.Command{
		Use:   "park",
		Short: `park every run at its next node boundary — "closing the lid"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			if err := client.Pause(ctx); err != nil {
				return err
			}
			if !wait {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "paused")
				return err
			}
			deadline := time.Now().Add(25 * time.Second)
			for time.Now().Before(deadline) {
				st, err := client.Status(ctx)
				if err != nil {
					return err
				}
				if st.InFlight == 0 {
					_, err = fmt.Fprintln(cmd.OutOrStdout(), "parked")
					return err
				}
				time.Sleep(200 * time.Millisecond)
			}
			return &usageError{msg: "timed out waiting for in-flight node executions to finish"}
		},
	}
	cmd.Flags().BoolVar(&wait, "wait", false, "block until every in-flight node execution has finished")
	return cmd
}
