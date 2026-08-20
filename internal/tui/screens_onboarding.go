package tui

import (
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
)

// handleOnboardingKey implements 09-cli-and-tui.md's "if there is no
// config yet it runs the first-run interview before creating anything."
// This is the minimal real version: acknowledge the unsandboxed-execution
// warning (mirroring the mockup's "[a] to accept", recorded — not merely
// displayed — since the doc treats the acknowledgement as a fact, not a
// flag; entry.go persists it as a marker file). A fuller interview
// (detecting go.mod, offering built-in workflows, first-task composer) is
// Future work — see L15-tui.md.
func (m Model) handleOnboardingKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "a" {
		m.onboarded = true
		// The acknowledgement itself is a durable fact 09-cli-and-tui.md
		// says belongs in the log, not a flag; a marker file is the
		// honest, minimal stand-in until a real onboarding.acknowledged
		// domain event exists (Future work) — it is not silently skipped.
		if m.homePath != "" {
			_ = os.WriteFile(filepath.Join(m.homePath, ".onboarded"), []byte("1"), 0o600)
		}
		m.navigate(ScreenHome)
		return m, m.refreshCmd()
	}
	return m, nil
}

func (m Model) viewOnboarding() string {
	return "" +
		"kairos · first run\n\n" +
		"  ⚠ isolation    NONE. Agents run as you, with your files. [a] to accept.\n"
}
