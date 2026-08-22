package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// handleSessionsKey drives the session list and the start-session flow —
// two plain text fields (project name, actor), matching `kairos session
// start --project --actor`'s own shape exactly (no project-picker widget:
// a session binds to a Project by name, the same as the CLI verb).
func (m Model) handleSessionsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.sessions.starting {
		return m.handleSessionStartKey(msg)
	}
	switch msg.String() {
	case "j":
		if m.sessions.cursor < len(m.sessions.sessions)-1 {
			m.sessions.cursor++
		}
	case "k":
		if m.sessions.cursor > 0 {
			m.sessions.cursor--
		}
	case "gg":
		m.sessions.cursor = 0
	case "G":
		m.sessions.cursor = len(m.sessions.sessions) - 1
	case "n":
		m.sessions.starting = true
		m.sessions.startField = 0
		m.sessions.startProject = ""
		m.sessions.startActor = ""
		m.sessions.startErr = nil
		m.mode = ModeINPUT
	case "enter":
		if m.sessions.cursor >= 0 && m.sessions.cursor < len(m.sessions.sessions) {
			id := m.sessions.sessions[m.sessions.cursor].ID
			return m.navigateToSessionChat(id)
		}
	}
	return m, nil
}

// navigateToSessionChat is the ONE place a session id enters
// sessionChatState — every subsequent key in that screen reads it back
// from there, never re-derives or re-collects it (sessionChatState's own
// doc comment explains why that matters).
func (m Model) navigateToSessionChat(sessionID string) (tea.Model, tea.Cmd) {
	m.navigate(ScreenSessionChat)
	m.sessionChat = sessionChatState{sessionID: sessionID}
	return m, m.fetchSessionChat(sessionID)
}

func (m Model) handleSessionStartKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.sessions.starting = false
		m.mode = ModeNAV
		return m, nil
	case "tab":
		if m.sessions.startField == 0 {
			m.sessions.startField = 1
		} else {
			m.sessions.startField = 0
		}
		return m, nil
	case "enter":
		if m.sessions.startField == 0 {
			m.sessions.startField = 1
			return m, nil
		}
		if m.sessions.startProject == "" {
			return m, nil
		}
		return m, m.startSession(m.sessions.startProject, m.sessions.startActor)
	case "backspace":
		if m.sessions.startField == 0 {
			m.sessions.startProject = trimLastRune(m.sessions.startProject)
		} else {
			m.sessions.startActor = trimLastRune(m.sessions.startActor)
		}
		return m, nil
	default:
		if len(msg.Runes) > 0 {
			if m.sessions.startField == 0 {
				m.sessions.startProject += string(msg.Runes)
			} else {
				m.sessions.startActor += string(msg.Runes)
			}
		}
		return m, nil
	}
}

func (m Model) viewSessions() string {
	var b strings.Builder
	b.WriteString("SESSIONS\n\n")
	if m.sessions.starting {
		return m.viewSessionStart(&b)
	}
	if m.sessions.err != nil {
		fmt.Fprintf(&b, "error: %v\n", m.sessions.err)
	}
	if len(m.sessions.sessions) == 0 {
		b.WriteString("no sessions yet — press n to start one\n")
	}
	for i, s := range m.sessions.sessions {
		cursor := " "
		if i == m.sessions.cursor {
			cursor = "▸"
		}
		fmt.Fprintf(&b, "%s %-30s %-8s runs=%-3d last used %s\n", cursor, s.ID, s.Actor, s.RunCount, s.LastUsedAt)
	}
	b.WriteString("\nNAV  j/k move  ⏎ open  n new session  h home  esc\n")
	if m.statusLine != "" {
		fmt.Fprintf(&b, "\n%s\n", m.statusLine)
	}
	return b.String()
}

func (m Model) viewSessionStart(b *strings.Builder) string {
	b.WriteString("new session\n\n")
	nameCursor, actorCursor := "", ""
	if m.mode == ModeINPUT {
		if m.sessions.startField == 0 {
			nameCursor = "█"
		} else {
			actorCursor = "█"
		}
	}
	fmt.Fprintf(b, "project: %s%s\n", m.sessions.startProject, nameCursor)
	fmt.Fprintf(b, "actor:   %s%s  (blank = default)\n", m.sessions.startActor, actorCursor)
	if m.sessions.startErr != nil {
		fmt.Fprintf(b, "\nerror: %v\n", m.sessions.startErr)
	}
	b.WriteString("\nINPUT  tab switch field  ⏎ next/start  esc cancel\n")
	return b.String()
}
