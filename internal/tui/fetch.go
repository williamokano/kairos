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

// doResultMsg is the Home composer's real result — `kairos do` closing
// L15-tui.md's own Future work stub (submitComposer used to be
// intentionally fake; see keys.go's doc comment on the real version).
type doResultMsg struct {
	resp cli.DoResponse
	err  error
}

type projectsFetchedMsg struct {
	projects []cli.Project
	err      error
}

type projectCreatedMsg struct {
	project cli.Project
	err     error
}

type fsBrowsedMsg struct {
	resp cli.FSBrowseResponse
	err  error
}

type sessionsFetchedMsg struct {
	sessions []cli.Session
	err      error
}

type sessionStartedMsg struct {
	session cli.Session
	err     error
}

// sessionChatFetchedMsg carries both the Session record (for its header —
// actor, workdir, branch) and its full message history in one round trip,
// mirroring the web UI's session-centric chat page's own combined fetch.
type sessionChatFetchedMsg struct {
	session  cli.Session
	messages []cli.ConversationMessage
	err      error
}

type sessionDoResultMsg struct {
	resp cli.DoResponse
	err  error
}

type sessionEndedMsg struct{ err error }

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
	case ScreenProjects:
		return m.fetchProjects()
	case ScreenSessions:
		return m.fetchSessions()
	case ScreenSessionChat:
		if m.sessionChat.sessionID != "" {
			return m.fetchSessionChat(m.sessionChat.sessionID)
		}
	}
	return nil
}

func (m Model) fetchProjects() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := withTimeout(m.ctx)
		defer cancel()
		projects, err := m.client.ListProjects(ctx)
		return projectsFetchedMsg{projects: projects, err: err}
	}
}

func (m Model) createProject(name, path string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := withTimeout(m.ctx)
		defer cancel()
		project, err := m.client.CreateProject(ctx, name, path)
		return projectCreatedMsg{project: project, err: err}
	}
}

func (m Model) browseFS(path string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := withTimeout(m.ctx)
		defer cancel()
		resp, err := m.client.BrowseFS(ctx, path)
		return fsBrowsedMsg{resp: resp, err: err}
	}
}

func (m Model) fetchSessions() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := withTimeout(m.ctx)
		defer cancel()
		sessions, err := m.client.ListSessions(ctx)
		return sessionsFetchedMsg{sessions: sessions, err: err}
	}
}

func (m Model) startSession(projectName, actor string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := withTimeout(m.ctx)
		defer cancel()
		session, err := m.client.StartSession(ctx, projectName, actor)
		return sessionStartedMsg{session: session, err: err}
	}
}

// fetchSessionChat combines GetSession (for the header/ConversationRunID)
// and GetConversation into the one message this screen needs — see
// sessionChatFetchedMsg's doc comment.
func (m Model) fetchSessionChat(sessionID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := withTimeout(m.ctx)
		defer cancel()
		session, err := m.client.GetSession(ctx, sessionID)
		if err != nil {
			return sessionChatFetchedMsg{err: err}
		}
		if session.ConversationRunID == "" {
			// A session with no turns yet has no conversation to fetch —
			// a real, empty-but-valid state, not an error.
			return sessionChatFetchedMsg{session: session}
		}
		msgs, err := m.client.GetConversation(ctx, session.ConversationRunID)
		return sessionChatFetchedMsg{session: session, messages: msgs, err: err}
	}
}

// sendSessionMessage always carries m.sessionChat.sessionID explicitly —
// the one thing this whole screen exists to make structurally impossible
// to drop (see sessionChatState's doc comment).
func (m Model) sendSessionMessage(sessionID, text string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := withTimeout(m.ctx)
		defer cancel()
		resp, err := m.client.DoWithSession(ctx, text, "", sessionID)
		return sessionDoResultMsg{resp: resp, err: err}
	}
}

// endSession is only ever constructed after the screen's own two-step
// confirm flow (keys_sessionchat.go) has collected a real reason and a
// typed confirmation — never reachable from a bare keypress. The server
// re-checks confirm == sessionID regardless (internal/api's existing
// Cancel/Fork/EndSession discipline: client-side gating is a UX aid, the
// server decides).
func (m Model) endSession(sessionID, reason, confirm string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := withTimeout(m.ctx)
		defer cancel()
		err := m.client.EndSession(ctx, sessionID, reason, confirm)
		return sessionEndedMsg{err: err}
	}
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
