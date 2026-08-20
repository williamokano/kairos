package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (m Model) handleConversationKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "i":
		m.mode = ModeINPUT
	}
	return m, nil
}

func (m Model) viewConversation() string {
	var b strings.Builder
	fmt.Fprintf(&b, "CONVERSATION · %s\n\n", m.conversation.runID)
	if m.conversation.err != nil {
		fmt.Fprintf(&b, "error: %v\n", m.conversation.err)
	}
	for _, msg := range m.conversation.messages {
		fmt.Fprintf(&b, "%s: %s\n", msg.Role, msg.Text)
	}
	b.WriteString("\n▏")
	b.WriteString(m.conversation.reply)
	if m.mode == ModeINPUT {
		b.WriteString("█")
	}
	b.WriteString("\n\nNAV  i reply  esc\n")
	return b.String()
}
