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

	// The directory picker (screens_projects.go) runs in NAV mode (j/k/
	// enter/u/s), which would otherwise let the global switch below steal
	// 'esc'/'s' before this sub-flow ever sees them — the exact same
	// reason ScreenDecision gets an early, full bypass above, not a
	// per-key carve-out.
	if m.screen == ScreenProjects && m.projects.creating && m.projects.picking {
		return m.handleProjectCreateKey(msg)
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
	case "p":
		m.navigate(ScreenProjects)
		return m, m.fetchProjects()
	case "s":
		// The directory picker's own "select this dir" 's' never reaches
		// here — handleKey's early bypass above routes every key
		// straight to handleProjectCreateKey while picking is active.
		m.navigate(ScreenSessions)
		return m, m.fetchSessions()
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
	if m.screen == ScreenProjects {
		return m.handleProjectsKey(msg)
	}
	if m.screen == ScreenSessions {
		return m.handleSessionsKey(msg)
	}
	if m.screen == ScreenFlowCreate {
		return m.handleFlowCreateKey(msg)
	}
	if m.screen == ScreenSourceCreate {
		return m.handleSourceCreateKey(msg)
	}
	if m.screen == ScreenSessionChat {
		if m.sessionChat.ending {
			return m.handleSessionEndKey(msg)
		}
		return m.handleSessionChatInputKey(msg)
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
		if m.conversation.isAdHoc {
			// A real wait: conversation node's run stays non-terminal
			// while it waits; an ad hoc chat's run is already terminal
			// after turn one, so continuing it needs a genuinely NEW
			// LLM turn, not a plain message append — see
			// conversationState.isAdHoc's doc comment.
			return m, func() tea.Msg {
				ctx, cancel := withTimeout(m.ctx)
				defer cancel()
				resp, err := m.client.Do(ctx, text, runID)
				return doResultMsg{resp: resp, err: err}
			}
		}
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

// submitComposer calls the real `kairos do` endpoint (POST /do,
// internal/api/do.go) — the stub this used to be (L15-tui.md's own
// Future work item) is closed: the TUI is now the SAME third client of
// that one entrypoint the web chat page and the CLI verb already are.
// Navigation to the Conversation screen happens once doResultMsg lands
// (model.go), not here — this only fires the request.
func (m Model) submitComposer() (tea.Model, tea.Cmd) {
	text := m.home.compose
	m.mode = ModeNAV
	m.home.compose = ""
	if text == "" {
		return m, nil
	}
	m.statusLine = "starting…"
	return m, func() tea.Msg {
		ctx, cancel := withTimeout(m.ctx)
		defer cancel()
		resp, err := m.client.Do(ctx, text, "")
		return doResultMsg{resp: resp, err: err}
	}
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
	case ScreenProjects:
		return m.handleProjectsKey(msg)
	case ScreenSessions:
		return m.handleSessionsKey(msg)
	case ScreenSessionChat:
		return m.handleSessionChatKey(msg)
	case ScreenFlowCreate:
		return m.handleFlowCreateKey(msg)
	case ScreenSourceCreate:
		return m.handleSourceCreateKey(msg)
	}
	return m, nil
}
