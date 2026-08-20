package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/williamokano/kairos/internal/decisionctx"
)

// defaultEventsReadTimeout bounds how long fetchDecisionContext waits for a
// run's historical event replay to finish (internal/cli.Client.Events).
const defaultEventsReadTimeout = 3 * time.Second

// decisionContext/findingSummary are aliases onto internal/decisionctx
// (L20) — the TUI and the web UI (L20) compute decision evidence from the
// identical function so the two surfaces can never silently diverge on
// what "risk" means. See decisionctx's package doc.
type decisionContext = decisionctx.Context
type findingSummary = decisionctx.FindingSummary

type decisionContextFetchedMsg struct {
	runID, nodeID, execID string
	ctx                   decisionContext
	err                   error
}

// openDecision navigates to the decision screen for one waiting node and
// kicks off evidence fetch. Every prior-pane "viewed" flag starts false —
// the decision pane is unreachable until each has been on screen at least
// once, per the anti-rubber-stamp focus-order rule.
func (m *Model) openDecision(runID, nodeID, execID string) tea.Cmd {
	m.navigate(ScreenDecision)
	m.decision = decisionState{
		runID: runID, nodeID: nodeID, execID: execID,
		focus:        paneRisk,
		riskAccepted: map[string]bool{},
	}
	return m.fetchDecisionContext(runID, nodeID, execID)
}

func (m Model) fetchDecisionContext(runID, nodeID, execID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := withTimeout(m.ctx)
		defer cancel()
		envs, err := m.client.Events(ctx, runID, defaultEventsReadTimeout)
		if err != nil {
			return decisionContextFetchedMsg{runID: runID, nodeID: nodeID, execID: execID, err: err}
		}
		return decisionContextFetchedMsg{runID: runID, nodeID: nodeID, execID: execID, ctx: decisionctx.Build(envs, nodeID)}
	}
}

func (m Model) applyDecisionContextFetched(msg decisionContextFetchedMsg) (Model, tea.Cmd) {
	if msg.runID != m.decision.runID || msg.nodeID != m.decision.nodeID {
		return m, nil // stale fetch from a screen we've since left
	}
	if msg.err != nil {
		m.decision.evidenceErr = msg.err
		return m, nil
	}
	m.decision.ctx = msg.ctx
	m.decision.evidenceErr = nil
	return m, nil
}
