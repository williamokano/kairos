package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/williamokano/kairos/internal/tasksource"
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
	var schedule string
	var weekday, hour, minute int
	add := &cobra.Command{
		Use:   "add <id>",
		Short: "register a trigger source",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if kind == "" {
				return &usageError{msg: "--kind is required"}
			}
			// Friendly per-kind flags — 08-triggers.md's own named Future
			// work ("--config takes raw JSON... a friendlier per-kind
			// flag surface is cosmetic, deferred"), closed here for
			// "cron", the one kind that's a real, constructible Source
			// today (see internal/tasksource.BuildCronConfig's doc
			// comment — github/jira/linear/plugin are not registered
			// anywhere in this tree yet, so friendly flags for them
			// would configure a source that silently never polls
			// anything). --config remains available for any other kind,
			// or to hand-author cron's own JSON directly.
			if kind == "cron" && config == "{}" && schedule != "" {
				built, err := tasksource.BuildCronConfig(schedule, weekday, hour, minute)
				if err != nil {
					return &usageError{msg: err.Error()}
				}
				config = built
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
	add.Flags().StringVar(&config, "config", "{}", "source config, as JSON (ignored for --kind cron if --schedule is set)")
	add.Flags().StringVar(&flow, "flow", "", "workflow definition path to run for this source's items")
	add.Flags().StringVar(&project, "project", "", "default project")
	add.Flags().IntVar(&interval, "interval", 120, "poll interval in seconds")
	add.Flags().StringVar(&schedule, "schedule", "", `for --kind cron: "daily" or "weekly"`)
	add.Flags().IntVar(&weekday, "weekday", 0, "for --kind cron --schedule weekly: 0=Sunday..6=Saturday")
	add.Flags().IntVar(&hour, "hour", 0, "for --kind cron: hour of day, 0-23")
	add.Flags().IntVar(&minute, "minute", 0, "for --kind cron: minute of hour, 0-59")
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
