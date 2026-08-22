package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// handleFlowCreateKey is ScreenFlowCreate's own two-step flow: a name,
// then a local file path this process reads itself and posts as yaml
// text — the TUI's real answer to "there is no way to create a workflow
// definition anywhere in this system" (until now a workflow could only
// be REFERENCED by a file that already existed on disk). esc at either
// step cancels, matching this codebase's "esc always, unconditionally,
// returns" rule.
func (m Model) handleFlowCreateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if !m.flows.creating {
		m.flows.creating = true
		m.flows.field = 0
		m.flows.name = ""
		m.flows.path = ""
		m.flows.saveErr = nil
		m.flows.saved = ""
		m.mode = ModeINPUT
		return m, nil
	}

	switch msg.String() {
	case "esc":
		m.flows.creating = false
		m.mode = ModeNAV
		return m, nil
	case "enter":
		if m.flows.field == 0 {
			if m.flows.name == "" {
				return m, nil
			}
			m.flows.field = 1
			return m, nil
		}
		if m.flows.path == "" {
			return m, nil
		}
		return m, m.createFlow(m.flows.name, m.flows.path)
	case "backspace":
		if m.flows.field == 0 {
			m.flows.name = trimLastRune(m.flows.name)
		} else {
			m.flows.path = trimLastRune(m.flows.path)
		}
		return m, nil
	default:
		if len(msg.Runes) > 0 {
			if m.flows.field == 0 {
				m.flows.name += string(msg.Runes)
			} else {
				m.flows.path += string(msg.Runes)
			}
		}
		return m, nil
	}
}

func (m Model) viewFlowCreate() string {
	var b strings.Builder
	b.WriteString("NEW FLOW\n\n")
	if m.flows.saved != "" {
		fmt.Fprintf(&b, "saved: %s\n\nn new flow  h home  esc\n", m.flows.saved)
		return b.String()
	}
	fmt.Fprintf(&b, "name: %s", m.flows.name)
	if m.flows.field == 0 && m.mode == ModeINPUT {
		b.WriteString("█")
	}
	b.WriteString("\n")
	if m.flows.field >= 1 {
		fmt.Fprintf(&b, "file path: %s", m.flows.path)
		if m.mode == ModeINPUT {
			b.WriteString("█")
		}
		b.WriteString("\n")
	}
	if m.flows.saveErr != nil {
		fmt.Fprintf(&b, "\nerror: %v\n", m.flows.saveErr)
	}
	if m.flows.field == 0 {
		b.WriteString("\nINPUT  ⏎ next: file path  esc cancel\n")
	} else {
		b.WriteString("\nINPUT  ⏎ save  esc cancel\n")
	}
	return b.String()
}
