package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newSrcCmd is `kairos src add/ls/pause/resume` (08-triggers.md) —
// registering and controlling trigger sources. It never talks to a
// plugin process directly; it only edits the daemon-owned `source` row,
// which internal/tasksource.Manager's own goroutines (started at boot)
// read.
func newSrcCmd(app *appCtx) *cobra.Command {
	root := &cobra.Command{Use: "src", Short: "manage trigger sources"}

	var kind, config, flow, project string
	var interval int
	add := &cobra.Command{
		Use:   "add <id>",
		Short: "register a trigger source",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if kind == "" {
				return &usageError{msg: "--kind is required"}
			}
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			src, err := client.AddSource(ctx, args[0], kind, config, flow, project, interval)
			if err != nil {
				return err
			}
			if app.output == "json" {
				return printJSON(cmd.OutOrStdout(), src)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\tenabled\n", src.ID, src.Kind)
			return err
		},
	}
	add.Flags().StringVar(&kind, "kind", "", "source kind (required)")
	add.Flags().StringVar(&config, "config", "{}", "source config, as JSON")
	add.Flags().StringVar(&flow, "flow", "", "workflow definition path to run for this source's items")
	add.Flags().StringVar(&project, "project", "", "default project")
	add.Flags().IntVar(&interval, "interval", 120, "poll interval in seconds")
	root.AddCommand(add)

	root.AddCommand(&cobra.Command{
		Use:   "ls",
		Short: "list trigger sources",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			sources, err := client.ListSources(ctx)
			if err != nil {
				return err
			}
			if app.output == "json" {
				return printJSON(cmd.OutOrStdout(), sources)
			}
			tw := newTable(cmd.OutOrStdout())
			_, _ = fmt.Fprintln(tw, "ID\tKIND\tENABLED\tHEALTH\tFLOW")
			for _, s := range sources {
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%v\t%s\t%s\n", s.ID, s.Kind, s.Enabled, s.Health, s.Flow)
			}
			return tw.Flush()
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "pause <id>",
		Short: "stop polling a source without deleting it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			if err := client.PauseSource(ctx, args[0]); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "paused")
			return err
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "resume <id>",
		Short: "resume polling a paused source",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := ensureClient(cmd, app)
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd)
			defer cancel()
			if err := client.ResumeSource(ctx, args[0]); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "resumed")
			return err
		},
	})

	return root
}
