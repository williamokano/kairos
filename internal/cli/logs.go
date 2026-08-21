package cli

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/spf13/cobra"
)

// newLogsCmd is L04-daemon-api-cli.md's deferred verb, built here: `kairos
// logs <runID>` prints a run's durable event log; `--follow` keeps the
// connection open and prints each new event the instant it lands, via the
// exact same GET /events SSE endpoint internal/tui's live subscription
// uses (Client.Events for the bounded read, Client.FollowEvents for the
// live tail with reconnect/resume). There is no separate route: this verb
// is a client of the SSE plumbing L04/L14/L18 already proved, not a new
// daemon-side capability, which is why apispec.Ops maps it onto the
// existing GET /events operation rather than minting a new one.
func newLogsCmd(app *appCtx) *cobra.Command {
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs <runID>",
		Short: "show a run's event log, or --follow to live-tail it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			runID := args[0]
			w := cmd.OutOrStdout()

			if !follow {
				ctx, cancel := withTimeout(cmd)
				defer cancel()
				envs, err := client.Events(ctx, runID, 2*time.Second)
				if err != nil {
					return err
				}
				for _, env := range envs {
					if err := printLogLine(w, app.output, env); err != nil {
						return err
					}
				}
				return nil
			}

			// --follow never returns on its own (the connection is meant
			// to stay open indefinitely); Ctrl-C is the intended exit,
			// ended cleanly rather than by an abrupt process kill.
			// internal/cli may not import syscall (only
			// internal/executor/local, internal/executor/exectest, and
			// cmd/kairos may — TestArchitecture_noExecOutsideExecutor),
			// so this catches os.Interrupt only, not SIGTERM; cmd/kairos's
			// own daemon lifecycle is where SIGTERM handling belongs.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()

			var followErr error
			err = client.FollowEvents(ctx, runID, 0, func(env Envelope) {
				if followErr != nil {
					return
				}
				followErr = printLogLine(w, app.output, env)
			}, func(status FollowStatus) {
				if status == FollowDisconnected {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "logs --follow: disconnected, reconnecting...")
				}
			})
			if followErr != nil {
				return followErr
			}
			return err
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep streaming new events as they land (SSE)")
	return cmd
}

// printLogLine renders one Envelope as either a JSON line (-o json — "a
// contract, not the table") or a compact human line: sequence, stream,
// event type, and the raw event payload.
func printLogLine(w io.Writer, output string, env Envelope) error {
	if output == "json" {
		return printJSON(w, env)
	}
	_, err := fmt.Fprintf(w, "%d %s %s %s\n", env.GlobalSeq, env.StreamID, env.EventType, string(env.Event))
	return err
}
