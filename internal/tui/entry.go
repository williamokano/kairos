package tui

import (
	"context"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/williamokano/kairos/internal/cli"
)

// Run starts the full-screen bubbletea program against the daemon at
// sockPath, blocking until the user detaches (q) or quits (Q). homePath is
// $KAIROS_HOME, used only to read/write the onboarding-acknowledgement
// marker (screens_onboarding.go).
func Run(ctx context.Context, sockPath, homePath string) error {
	client := cli.NewClient(sockPath)
	onboarded := isOnboarded(homePath)
	m := New(ctx, client, homePath, onboarded)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func isOnboarded(homePath string) bool {
	if homePath == "" {
		return true
	}
	_, err := os.Stat(filepath.Join(homePath, ".onboarded"))
	return err == nil
}
