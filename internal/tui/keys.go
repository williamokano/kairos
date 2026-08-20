package tui

import tea "github.com/charmbracelet/bubbletea"

// handleKey is the single entry point for every keystroke. It never lets a
// single keypress mutate anything destructive: x/f/Q all prompt (a
// confirm sub-state), and nothing here can answer a human decision except
// by first reaching the decision screen through openDecision, which itself
// enforces the focus-order/typed-word rules in keys_decision.go.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.paletteOpen {
		return m.handlePaletteKey(msg)
	}

	if m.screen == ScreenDecision {
		nm, cmd := m.handleDecisionKey(msg)
		return nm, cmd
	}

	if m.screen == ScreenOnboarding {
		return m.handleOnboardingKey(msg)
	}

	if m.mode == ModeINPUT {
		return m.handleGlobalInputKey(msg)
	}

	// Global NAV-mode bindings, available on every screen.
	switch msg.String() {
	case ":":
		m.paletteOpen = true
		m.paletteInput = ""
		return m, nil
	case "h":
		m.navigate(ScreenHome)
		return m, m.refreshCmd()
	case "a":
		m.navigate(ScreenInbox)
		return m, m.fetchInbox()
	case "r":
		if m.screen != ScreenHome { // 'r' is also "runs" nav; on Home it's reserved by the composer's 'i' path elsewhere
			m.navigate(ScreenHome)
			return m, m.fetchRuns()
		}
	case "l":
		if m.runInspector.runID != "" {
			m.navigate(ScreenLogs)
			m.logs.runID = m.runInspector.runID
		}
		return m, nil
	case "q":
		m.quitting = true
		return m, tea.Quit
	case "Q":
		// "refuses with a prompt if runs are active" — a minimal, real
		// confirmation: require pressing Q twice in NAV mode is NOT what
		// we do (that's a hidden double-tap, easy to trigger by accident);
		// instead a literal y/n prompt line is shown and consumed here.
		if m.hasActiveRuns() {
			m.statusLine = "runs are active — quit and stop the engine anyway? y/n"
			m.paletteOpen = false
			m.mode = ModeNAV
			return m.withPendingQuitConfirm(), nil
		}
		m.quitting = true
		return m, tea.Quit
	case "y":
		if m.statusLine != "" && m.pendingQuitConfirm {
			m.pendingQuitConfirm = false
			m.quitting = true
			return m, tea.Quit
		}
	case "n":
		if m.pendingQuitConfirm {
			m.pendingQuitConfirm = false
			m.statusLine = ""
			return m, nil
		}
	case "esc":
		m.back()
		return m, nil
	}

	return m.dispatchScreenKey(msg)
}

func (m Model) withPendingQuitConfirm() Model {
	m.pendingQuitConfirm = true
	return m
}

func (m Model) hasActiveRuns() bool {
	for _, r := range m.home.runs {
		if !isTerminalStatus(r.Status) {
			return true
		}
	}
	return false
}

func (m Model) handleGlobalInputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.screen == ScreenConversation {
		return m.handleConversationInputKey(msg)
	}
	if m.screen == ScreenBenchmark {
		return m.handleBenchmarkInputKey(msg)
	}

	switch msg.String() {
	case "esc":
		m.mode = ModeNAV
		return m, nil
	case "enter":
		return m.submitComposer()
	case "backspace":
		m.home.compose = trimLastRune(m.home.compose)
		return m, nil
	default:
		if len(msg.Runes) > 0 {
			m.home.compose += string(msg.Runes)
		}
		return m, nil
	}
}

func (m Model) handleConversationInputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = ModeNAV
		return m, nil
	case "enter":
		text := m.conversation.reply
		m.conversation.reply = ""
		m.mode = ModeNAV
		if text == "" {
			return m, nil
		}
		runID := m.conversation.runID
		return m, func() tea.Msg {
			ctx, cancel := withTimeout(m.ctx)
			defer cancel()
			err := m.client.PostConversationMessage(ctx, runID, text)
			return conversationSentMsg{err: err}
		}
	case "backspace":
		m.conversation.reply = trimLastRune(m.conversation.reply)
		return m, nil
	default:
		if len(msg.Runes) > 0 {
			m.conversation.reply += string(msg.Runes)
		}
		return m, nil
	}
}

// submitComposer is intentionally a stub: 09-cli-and-tui.md's `kairos do`
// (start a run from prose) has no daemon-side endpoint yet — internal/api
// only accepts POST /runs with a definitionPath, not free text. Recorded
// honestly as Future work rather than faked.
func (m Model) submitComposer() (tea.Model, tea.Cmd) {
	m.mode = ModeNAV
	m.statusLine = "starting a run from prose (kairos do) has no daemon endpoint yet — see L15-tui.md's Future work"
	m.home.compose = ""
	return m, nil
}

func (m Model) dispatchScreenKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.screen {
	case ScreenHome:
		return m.handleHomeKey(msg)
	case ScreenInbox:
		return m.handleInboxKey(msg)
	case ScreenRunInspector:
		return m.handleRunInspectorKey(msg)
	case ScreenConversation:
		return m.handleConversationKey(msg)
	case ScreenLogs:
		return m.handleLogsKey(msg)
	case ScreenRunners:
		return m, nil
	case ScreenBenchmark:
		if msg.String() == "i" {
			m.mode = ModeINPUT
		}
		return m, nil
	}
	return m, nil
}
