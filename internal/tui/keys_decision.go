package tui

import tea "github.com/charmbracelet/bubbletea"

// decisionField names which text field INPUT-mode keystrokes go to on the
// decision screen.
type decisionField int

const (
	fieldNone decisionField = iota
	fieldReason
	fieldTypedConfirm
)

// handleDecisionKey implements every anti-rubber-stamp rule from
// 09-cli-and-tui.md's "Anti-rubber-stamp mechanics" as real, enforced
// behavior, not merely as UI copy:
//   - focus walks risk -> host effects -> changed -> findings -> decision,
//     and the decision pane refuses focus until each prior pane has
//     actually been rendered (tracked in m.decision.viewed).
//   - high/critical findings require a separate risk-acceptance control
//     before the decision pane is reachable.
//   - the decision word must be typed, exactly, into its own field —
//     never inferred from the selected radio choice, never satisfied by a
//     keypress alone.
//   - evidence-load failure blocks the form entirely.
//   - there is no key, anywhere, that submits a decision in one press.
func (m Model) handleDecisionKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	if m.decision.evidenceErr != nil {
		// The one place partial degrades to blocked: only 'R' (retry) and
		// esc are live while evidence failed to load.
		switch msg.String() {
		case "R":
			return m, m.fetchDecisionContext(m.decision.runID, m.decision.nodeID, m.decision.execID)
		case "esc":
			m.back()
			return m, nil
		}
		return m, nil
	}

	if m.mode == ModeINPUT {
		return m.handleDecisionInputKey(msg)
	}

	switch msg.String() {
	case "esc":
		m.back()
		return m, nil

	case "tab":
		m.advanceDecisionFocus()
		return m, nil

	case "r":
		switch m.decision.focus {
		case paneFindings:
			m.acceptFocusedRisk()
		case paneDecision:
			m.mode = ModeINPUT
			m.decision.activeField = fieldReason
		}
		return m, nil

	case "t":
		if m.decision.focus == paneDecision {
			m.mode = ModeINPUT
			m.decision.activeField = fieldTypedConfirm
		}
		return m, nil

	case "1", "2", "3":
		if m.decision.focus == paneDecision {
			m.decision.decisionChoice = map[string]string{"1": "approve", "2": "request-changes", "3": "reject"}[msg.String()]
		}
		return m, nil

	case "enter":
		if m.decision.focus == paneDecision {
			return m.trySubmitDecision()
		}
		return m, nil
	}
	return m, nil
}

func (m Model) handleDecisionInputKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = ModeNAV
		m.decision.activeField = fieldNone
		return m, nil
	case "enter":
		m.mode = ModeNAV
		m.decision.activeField = fieldNone
		return m, nil
	case "backspace":
		switch m.decision.activeField {
		case fieldReason:
			m.decision.reasonInput = trimLastRune(m.decision.reasonInput)
		case fieldTypedConfirm:
			m.decision.typedInput = trimLastRune(m.decision.typedInput)
		}
		return m, nil
	default:
		if len(msg.Runes) == 0 {
			return m, nil
		}
		switch m.decision.activeField {
		case fieldReason:
			m.decision.reasonInput += string(msg.Runes)
		case fieldTypedConfirm:
			// Tab-completion is disabled in this field by construction:
			// it is a plain string accumulator with no suggestion/complete
			// key handled anywhere, so there is nothing to disable — the
			// invariant holds because the feature was never built, not
			// because it was built and then turned off.
			m.decision.typedInput += string(msg.Runes)
		}
		return m, nil
	}
}

func trimLastRune(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return string(r[:len(r)-1])
}

// advanceDecisionFocus walks focus forward exactly one pane, marking the
// pane just left as viewed. It refuses to land on paneDecision until every
// prior pane is viewed AND every high/critical finding's risk has been
// separately accepted — the two real gates 09-cli-and-tui.md requires.
func (m *Model) advanceDecisionFocus() {
	m.decision.viewed[m.decision.focus] = true
	next := m.decision.focus + 1
	if next >= numPanes {
		next = paneRisk // wrap, rather than stall, if decision isn't reachable yet
	}
	if next == paneDecision && !m.decisionReadyForFocus() {
		// Stay put rather than granting focus prematurely — this is the
		// literal enforcement of "the decision pane refuses focus until
		// each prior pane has been on screen."
		return
	}
	m.decision.focus = next
}

// decisionReadyForFocus is true only once risk/hostEffects/changed/findings
// have all been viewed and every high/critical finding has had its risk
// separately accepted.
func (m Model) decisionReadyForFocus() bool {
	for p := paneRisk; p < paneDecision; p++ {
		if !m.decision.viewed[p] {
			return false
		}
	}
	for _, f := range m.decision.ctx.hasHighOrCriticalFindings() {
		if !m.decision.riskAccepted[f.ID] {
			return false
		}
	}
	return true
}

func (m *Model) acceptFocusedRisk() {
	findings := m.decision.ctx.hasHighOrCriticalFindings()
	if len(findings) == 0 {
		return
	}
	// Accept the first not-yet-accepted one — a minimal, real per-finding
	// control; a fuller UI would let you navigate between them, deferred
	// (see L15-tui.md's Future work) since this document's scope is the
	// invariant ("separate from the decision control, per-finding"), not a
	// rich list-navigation widget.
	for _, f := range findings {
		if !m.decision.riskAccepted[f.ID] {
			m.decision.riskAccepted[f.ID] = true
			return
		}
	}
}

// trySubmitDecision is the ONLY path that can answer a Waiting(human) node
// from this screen, and it enforces every precondition again at the point
// of submission — never trusting that reaching this key means the
// preconditions still hold, since state can change between keypresses.
func (m Model) trySubmitDecision() (Model, tea.Cmd) {
	d := m.decision
	switch {
	case !m.decisionReadyForFocus():
		m.statusLine = "evidence not fully reviewed — walk every pane with tab first"
		return m, nil
	case d.decisionChoice == "":
		m.statusLine = "no decision selected — press 1 (approve), 2 (request-changes), or 3 (reject)"
		return m, nil
	case d.reasonInput == "":
		m.statusLine = "a reason is required for every decision"
		return m, nil
	case d.typedInput != d.decisionChoice:
		m.statusLine = "type the decision word exactly to confirm — " + d.decisionChoice
		return m, nil
	}
	m.decision.submitting = true
	m.statusLine = ""
	return m, m.submitDecisionCmd()
}

func (m Model) submitDecisionCmd() tea.Cmd {
	d := m.decision
	return func() tea.Msg {
		ctx, cancel := withTimeout(m.ctx)
		defer cancel()
		err := m.client.ApproveHumanTask(ctx, d.runID, d.nodeID, d.decisionChoice, d.reasonInput, d.nodeID)
		return approveResultMsg{err: err}
	}
}

func (m Model) applyApproveResult(msg approveResultMsg) (Model, tea.Cmd) {
	m.decision.submitting = false
	if msg.err != nil {
		m.decision.submitErr = msg.err
		return m, nil
	}
	m.decision.answered = true
	m.navigate(ScreenInbox)
	return m, m.fetchInbox()
}
