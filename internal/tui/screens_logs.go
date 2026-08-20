package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// handleLogsKey is intentionally minimal: 09-cli-and-tui.md's log follow
// screen reads a capped tail from disk via the daemon; no API endpoint
// currently streams stdout/stderr content (internal/artifact, L09, has no
// HTTP route yet). Recorded honestly as Future work rather than faked with
// placeholder lines.
func (m Model) handleLogsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	return m, nil
}

func (m Model) viewLogs() string {
	var b strings.Builder
	fmt.Fprintf(&b, "LOGS · %s\n\n", m.logs.runID)
	b.WriteString("no log-streaming API endpoint exists yet — see kairos show / stdout.log directly\n")
	b.WriteString("under $KAIROS_HOME/work/<runID>/<execID>/ in the meantime.\n")
	b.WriteString("\nNAV  esc\n")
	return b.String()
}
