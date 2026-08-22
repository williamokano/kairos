package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// handleSessionChatKey is the TUI's real answer to the web UI's
// /sessions/{id} page (L26-session-chat.md): one persistent screen per
// session, its id held in sessionChatState for the screen's whole
// lifetime (set once by navigateToSessionChat), never re-collected per
// message — the exact property the web UI's fragile two-step picker
// lacked before it was fixed.
func (m Model) handleSessionChatKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.sessionChat.ending {
		return m.handleSessionEndKey(msg)
	}
	switch msg.String() {
	case "i":
		m.mode = ModeINPUT
	case "x":
		// Matches keys.go's established rule: x/f/Q never execute
		// directly — they only ever open a confirm sub-state. Ending a
		// session discards its git worktree (ADR 0014's accepted
		// residual risk) — the same weight as `kairos cancel`/`kairos
		// fork`, so the same two-step typed confirmation, not a bare y/n.
		m.sessionChat.ending = true
		m.sessionChat.endStep = 0
		m.sessionChat.endReason = ""
		m.sessionChat.endConfirm = ""
		m.sessionChat.endErr = nil
		m.mode = ModeINPUT
	}
	return m, nil
}

// handleSessionChatInputKey is reached via handleGlobalInputKey (keys.go)
// when mode==INPUT and screen==ScreenSessionChat — the reply box.
func (m Model) handleSessionChatInputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = ModeNAV
		return m, nil
	case "enter":
		text := m.sessionChat.reply
		m.sessionChat.reply = ""
		m.mode = ModeNAV
		if text == "" {
			return m, nil
		}
		return m, m.sendSessionMessage(m.sessionChat.sessionID, text)
	case "backspace":
		m.sessionChat.reply = trimLastRune(m.sessionChat.reply)
		return m, nil
	default:
		if len(msg.Runes) > 0 {
			m.sessionChat.reply += string(msg.Runes)
		}
		return m, nil
	}
}

// handleSessionEndKey is the two-step typed confirmation: a reason, then
// the session id typed out again exactly — matching kairos session end's
// own CLI flags (--reason, --confirm <id>) and this project's
// established "no --yes/-f shortcut" discipline for exactly this class
// of action. The daemon operation is only ever called from step 1's
// submission, never from a bare keypress.
func (m Model) handleSessionEndKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.sessionChat.ending = false
		m.mode = ModeNAV
		return m, nil
	case "enter":
		if m.sessionChat.endStep == 0 {
			if m.sessionChat.endReason == "" {
				return m, nil
			}
			m.sessionChat.endStep = 1
			return m, nil
		}
		// Step 1: require the typed confirmation to equal the session id
		// exactly before ever calling the daemon — the server re-checks
		// this regardless (client-side gating is a UX aid, not the
		// enforcement point), but a bare "enter" here must never reach
		// the network call with an empty/wrong confirm.
		if m.sessionChat.endConfirm != m.sessionChat.sessionID {
			return m, nil
		}
		return m, m.endSession(m.sessionChat.sessionID, m.sessionChat.endReason, m.sessionChat.endConfirm)
	case "backspace":
		if m.sessionChat.endStep == 0 {
			m.sessionChat.endReason = trimLastRune(m.sessionChat.endReason)
		} else {
			m.sessionChat.endConfirm = trimLastRune(m.sessionChat.endConfirm)
		}
		return m, nil
	default:
		if len(msg.Runes) > 0 {
			if m.sessionChat.endStep == 0 {
				m.sessionChat.endReason += string(msg.Runes)
			} else {
				m.sessionChat.endConfirm += string(msg.Runes)
			}
		}
		return m, nil
	}
}

func (m Model) viewSessionChat() string {
	var b strings.Builder
	s := m.sessionChat.session
	fmt.Fprintf(&b, "SESSION · %s · %s · %s\n\n", m.sessionChat.sessionID, s.Actor, s.WorkDir)
	if m.sessionChat.ending {
		return m.viewSessionEnd(&b)
	}
	if m.sessionChat.err != nil {
		fmt.Fprintf(&b, "error: %v\n", m.sessionChat.err)
	}
	for _, msg := range m.sessionChat.messages {
		fmt.Fprintf(&b, "%s: %s\n", msg.Role, msg.Text)
	}
	b.WriteString("\n▏")
	b.WriteString(m.sessionChat.reply)
	if m.mode == ModeINPUT {
		b.WriteString("█")
	}
	b.WriteString("\n\nNAV  i reply  x end session  esc\n")
	if m.statusLine != "" {
		fmt.Fprintf(&b, "\n%s\n", m.statusLine)
	}
	return b.String()
}

func (m Model) viewSessionEnd(b *strings.Builder) string {
	b.WriteString("end this session? this discards its worktree — cannot be undone.\n\n")
	if m.sessionChat.endStep == 0 {
		fmt.Fprintf(b, "reason: %s", m.sessionChat.endReason)
		if m.mode == ModeINPUT {
			b.WriteString("█")
		}
		b.WriteString("\n\nINPUT  ⏎ next: type the session id to confirm  esc cancel\n")
		return b.String()
	}
	fmt.Fprintf(b, "reason: %s\n\n", m.sessionChat.endReason)
	fmt.Fprintf(b, "type %q to confirm: %s", m.sessionChat.sessionID, m.sessionChat.endConfirm)
	if m.mode == ModeINPUT {
		b.WriteString("█")
	}
	if m.sessionChat.endErr != nil {
		fmt.Fprintf(b, "\n\nerror: %v\n", m.sessionChat.endErr)
	}
	b.WriteString("\n\nINPUT  ⏎ end session  esc cancel\n")
	return b.String()
}
