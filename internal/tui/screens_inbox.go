package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// handleInboxKey implements 09-cli-and-tui.md's rule that the inbox "never
// answers a decision. It links to the decision screen and nothing else —
// no inline approve, no bulk approve, no keyboard shortcut that resolves
// an item from here." Enter opens the decision screen; nothing else in
// this file can answer one.
func (m Model) handleInboxKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "j":
		if m.inbox.cursor < len(m.inbox.items)-1 {
			m.inbox.cursor++
		}
	case "k":
		if m.inbox.cursor > 0 {
			m.inbox.cursor--
		}
	case "enter":
		if m.inbox.cursor >= 0 && m.inbox.cursor < len(m.inbox.items) {
			it := m.inbox.items[m.inbox.cursor]
			cmd := m.openDecision(it.RunID, it.NodeID, it.ExecID)
			return m, cmd
		}
	}
	return m, nil
}

func (m Model) viewInbox() string {
	var b strings.Builder
	fmt.Fprintf(&b, "INBOX · %d waiting\n\n", len(m.inbox.items))
	if m.inbox.err != nil {
		fmt.Fprintf(&b, "error: %v\n", m.inbox.err)
	}
	if len(m.inbox.items) == 0 {
		b.WriteString("nothing waiting on you\n")
	}
	for i, it := range m.inbox.items {
		cursor := " "
		if i == m.inbox.cursor {
			cursor = "▸"
		}
		fmt.Fprintf(&b, "%s %-14s node %-16s run %s\n", cursor, "waiting", it.NodeID, it.RunID)
	}
	b.WriteString("\nNAV  ⏎ open the decision   j/k   esc\n")
	return b.String()
}
