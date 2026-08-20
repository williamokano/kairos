package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (m Model) handleHomeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "j":
		if m.home.cursor < len(m.home.runs)-1 {
			m.home.cursor++
		}
	case "k":
		if m.home.cursor > 0 {
			m.home.cursor--
		}
	case "gg":
		m.home.cursor = 0
	case "G":
		m.home.cursor = len(m.home.runs) - 1
	case "i":
		m.mode = ModeINPUT
	case "enter":
		if m.home.cursor >= 0 && m.home.cursor < len(m.home.runs) {
			id := m.home.runs[m.home.cursor].RunID
			m.navigate(ScreenRunInspector)
			m.runInspector.runID = id
			return m, m.fetchRunState(id)
		}
	}
	return m, nil
}

func (m Model) viewHome() string {
	var b strings.Builder
	b.WriteString("HOME\n\n")
	if m.home.err != nil {
		fmt.Fprintf(&b, "error: %v\n", m.home.err)
	}
	if len(m.home.runs) == 0 {
		b.WriteString("no runs yet — press i and describe a task\n")
	}
	for i, r := range m.home.runs {
		cursor := " "
		if i == m.home.cursor {
			cursor = "▸"
		}
		fmt.Fprintf(&b, "%s %-30s %-10s started %s\n", cursor, r.RunID, r.Status, r.StartedAt)
	}
	b.WriteString("\n▏")
	b.WriteString(m.home.compose)
	if m.mode == ModeINPUT {
		b.WriteString("█")
	}
	b.WriteString("\n\nNAV  ⏎ open  j/k move  i new task  a inbox  r runs  : goto  Q quit\n")
	if m.statusLine != "" {
		fmt.Fprintf(&b, "\n%s\n", m.statusLine)
	}
	return b.String()
}
