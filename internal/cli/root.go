package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/williamokano/kairos/internal/config"
	"github.com/williamokano/kairos/internal/version"
)

// appCtx is the state every verb needs: the daemon client (built once
// $KAIROS_HOME is known) and the shared output format flag.
type appCtx struct {
	client   *Client
	sockPath string
	homePath string
	output   string
	starter  DaemonStarter
	serve    ServeFunc
	tui      TUIFunc
	web      WebFunc
}

// Execute builds and runs the root command, returning the process exit
// code. Only cmd/kairos/main.go may call os.Exit — this function just
// computes what that call's argument should be (AGENTS.md §2: "Only
// cmd/kairos/main.go may call os.Exit"). serve is the real daemon boot
// sequence and tui the real TUI entry point, both injected from
// cmd/kairos (see serve.go's ServeFunc/TUIFunc docs).
func Execute(args []string, starter DaemonStarter, serve ServeFunc, tui TUIFunc, web WebFunc) int {
	app := &appCtx{starter: starter, output: "table", serve: serve, tui: tui, web: web}
	root := newRootCmd(app)
	root.SetArgs(args)

	err := root.Execute()
	if err == nil {
		return 0
	}
	fmt.Fprintln(os.Stderr, "Error:", err)
	return exitCodeFor(err)
}

func exitCodeFor(err error) int {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case 404:
			return 3
		case 422:
			return 4
		case 500:
			return 8
		}
	}
	var usage *usageError
	if errors.As(err, &usage) {
		return 2
	}
	var notReady *daemonNotReadyError
	if errors.As(err, &notReady) {
		return 1
	}
	var invariant *invariantViolationError
	if errors.As(err, &invariant) {
		return 8
	}
	return 1
}

type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

type daemonNotReadyError struct{ err error }

func (e *daemonNotReadyError) Error() string { return "daemon not reachable: " + e.err.Error() }
func (e *daemonNotReadyError) Unwrap() error { return e.err }

// RootCommand builds the cobra command tree for introspection only (e.g.
// TestUI_everyCallHasCLICounterpart in internal/archtest) — its RunE
// funcs are wired but never invoked by callers that only walk the tree.
func RootCommand() *cobra.Command {
	return newRootCmd(&appCtx{output: "table"})
}

func newRootCmd(app *appCtx) *cobra.Command {
	root := &cobra.Command{
		Use:           "kairos",
		Short:         "Kairos: a durable, typed, event-sourced orchestration engine.",
		SilenceUsage:  true,
		SilenceErrors: true,
		// Bare `kairos`: attach the TUI when stdout is a real terminal;
		// otherwise print status and exit 0 — "kairos | grep must never
		// hang on a full-screen app" (09-cli-and-tui.md, L04's deferred
		// one-line change, made here).
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.tui != nil && isTTY(cmd.OutOrStdout()) {
				if _, err := ensureClient(cmd, app); err != nil {
					return err
				}
				return app.tui(cmd.Context(), app.sockPath, app.homePath)
			}
			return runStatus(cmd, app)
		},
	}
	root.PersistentFlags().StringVarP(&app.output, "output", "o", "table", "table | json")

	root.AddCommand(newVersionCmd())
	root.AddCommand(newServeCmd(app))
	root.AddCommand(newRunCmd(app))
	root.AddCommand(newDoCmd(app))
	root.AddCommand(newLsCmd(app))
	root.AddCommand(newShowCmd(app))
	root.AddCommand(newConversationCmd(app))
	root.AddCommand(newLogsCmd(app))
	root.AddCommand(newApproveCmd(app))
	root.AddCommand(newEffectsCmd(app))
	root.AddCommand(newWaiverCmd(app))
	root.AddCommand(newHumanTasksCmd(app))
	root.AddCommand(newForkCmd(app))
	root.AddCommand(newCancelCmd(app))
	root.AddCommand(newCompareCmd(app))
	root.AddCommand(newDiffCmd(app))
	root.AddCommand(newDoctorCmd(app))
	root.AddCommand(newDBCmd(app))
	root.AddCommand(newStatusCmd(app))
	root.AddCommand(newCheckOutputCmd())
	root.AddCommand(newPauseCmd(app))
	root.AddCommand(newResumeCmd(app))
	root.AddCommand(newParkCmd(app))
	root.AddCommand(newSrcCmd(app))
	root.AddCommand(newWebCmd(app))
	root.AddCommand(newCostCmd(app))
	return root
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

// ensureClient resolves $KAIROS_HOME, builds the daemon client, and
// (unless skipAutoStart) ensures a daemon is running — auto-starting one
// via app.starter if the socket doesn't respond, per decision #4.
func ensureClient(cmd *cobra.Command, app *appCtx) (*Client, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	sockPath := filepath.Join(cfg.Home, "daemon.sock")
	app.sockPath = sockPath
	app.homePath = cfg.Home
	client := NewClient(sockPath)
	app.client = client

	if err := ensureDaemon(cmd.Context(), client, app.starter, 5*time.Second); err != nil {
		return nil, &daemonNotReadyError{err: err}
	}
	return client, nil
}

func withTimeout(cmd *cobra.Command) (context.Context, context.CancelFunc) {
	return context.WithTimeout(cmd.Context(), 30*time.Second)
}

// isTTY reports whether w is a real terminal — a plain os.ModeCharDevice
// check, stdlib-only (no new dependency), matching the exact test bare
// `kairos` needs: "is a script piping me, or is a human watching."
func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
