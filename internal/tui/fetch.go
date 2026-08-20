package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/williamokano/kairos/internal/cli"
)

type runsFetchedMsg struct {
	runs []cli.RunSummary
	err  error
}

type runStateFetchedMsg struct {
	runID string
	state cli.RunState
	err   error
}

type conversationFetchedMsg struct {
	messages []cli.ConversationMessage
	err      error
}

// inboxItem is one entry in the Inbox — a NodeExecution in ExecWaiting on a
// non-terminal run, together with enough of the run's identity to route to
// the decision screen. 09-cli-and-tui.md's "waiting: reason" naming would
// need actor/wait-kind data this API doesn't expose yet (see
// L15-tui.md's Documented decisions), so here "waiting" always means
// ExecStatus=="waiting" — real, just less finely categorized than the doc's
// mockups show.
type inboxItem struct {
	RunID  string
	NodeID string
	ExecID string
}

type inboxFetchedMsg struct {
	items []inboxItem
	err   error
}

type doctorFetchedMsg struct {
	resp cli.DoctorResponse
	err  error
}

type approveResultMsg struct {
	err error
}

type conversationSentMsg struct{ err error }

// refreshCmd re-fetches whatever the current screen needs. This is the
// polling stand-in for live SSE push (doc.go).
func (m Model) refreshCmd() tea.Cmd {
	switch m.screen {
	case ScreenHome:
		return m.fetchRuns()
	case ScreenRunInspector:
		if m.runInspector.runID != "" {
			return m.fetchRunState(m.runInspector.runID)
		}
	case ScreenConversation:
		if m.conversation.runID != "" {
			return m.fetchConversation(m.conversation.runID)
		}
	case ScreenInbox:
		return m.fetchInbox()
	case ScreenRunners:
		return m.fetchDoctor()
	}
	return nil
}

func (m Model) fetchRuns() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := withTimeout(m.ctx)
		defer cancel()
		runs, err := m.client.ListRuns(ctx, "")
		return runsFetchedMsg{runs: runs, err: err}
	}
}

func (m Model) fetchRunState(runID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := withTimeout(m.ctx)
		defer cancel()
		state, err := m.client.GetRun(ctx, runID)
		return runStateFetchedMsg{runID: runID, state: state, err: err}
	}
}

func (m Model) fetchConversation(runID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := withTimeout(m.ctx)
		defer cancel()
		msgs, err := m.client.GetConversation(ctx, runID)
		return conversationFetchedMsg{messages: msgs, err: err}
	}
}

// fetchInbox scans every non-terminal run for a waiting NodeExecution —
// there is no dedicated "list decisions" endpoint (Documented decisions),
// so this is real but O(runs), acceptable at the scale a single local
// daemon operates at.
func (m Model) fetchInbox() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := withTimeout(m.ctx)
		defer cancel()
		runs, err := m.client.ListRuns(ctx, "")
		if err != nil {
			return inboxFetchedMsg{err: err}
		}
		var items []inboxItem
		for _, r := range runs {
			if isTerminalStatus(r.Status) {
				continue
			}
			state, err := m.client.GetRun(ctx, r.RunID)
			if err != nil {
				continue
			}
			for nodeID, execs := range state.Executions {
				for _, e := range execs {
					if e.Status == "waiting" {
						items = append(items, inboxItem{RunID: r.RunID, NodeID: nodeID, ExecID: e.ExecID})
					}
				}
			}
		}
		return inboxFetchedMsg{items: items}
	}
}

func (m Model) fetchDoctor() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := withTimeout(m.ctx)
		defer cancel()
		resp, err := m.client.Doctor(ctx)
		return doctorFetchedMsg{resp: resp, err: err}
	}
}

func isTerminalStatus(s string) bool {
	switch s {
	case "succeeded", "failed", "cancelled", "rejected":
		return true
	}
	return false
}
