// Command kairos is the daemon, the executor, the CLI, and the TUI, all in
// one binary. This is the only file in the repository allowed to call
// os.Exit or log.Fatal — a stray Fatal anywhere else would kill a run
// mid-dispatch (AGENTS.md §2).
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/williamokano/kairos/internal/version"
)

func main() {
	root := &cobra.Command{
		Use:           "kairos",
		Short:         "Kairos: a durable, typed, event-sourced orchestration engine.",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.AddCommand(newVersionCmd())

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "print the kairos build version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), version.String())
			return err
		},
	}
}
