package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/williamokano/kairos/internal/cli"
)

// Screen is one of the seven real screens 09-cli-and-tui.md names, plus the
// two structural states (onboarding, command palette) that are not
// themselves "screens" in the doc's list but need model states of their
// own.
type Screen int

const (
	ScreenHome Screen = iota
	ScreenConversation
	ScreenRunInspector
	ScreenLogs
	ScreenInbox
	ScreenRunners
	ScreenBenchmark
	ScreenDecision
	ScreenOnboarding
)

// Mode is the two-mode keyboard model 09-cli-and-tui.md requires: in NAV,
// single keys are commands; in INPUT, single keys are text. Esc always,
// unconditionally, returns to NAV — no exceptions, per the doc.
type Mode int

const (
	ModeNAV Mode = iota
	ModeINPUT
)

// Model is bubbletea's root model. It holds only view/navigation state and
// the last-fetched data — no execution state, no engine state; everything
// here is either UI-local (mode, focus, width/height) or a cache of what
// the API most recently returned.
type Model struct {
	client   *cli.Client
	ctx      context.Context
	homePath string // $KAIROS_HOME, used only to persist the onboarding acknowledgement marker

	width, height int
	screen        Screen
	prevScreen    Screen // for esc's "walk back up" rule
	mode          Mode

	onboarded bool // false only on a genuinely first run — see entry.go

	paletteOpen  bool
	paletteInput string
	statusLine   string // last error/info line shown in the status bar

	pendingQuitConfirm bool // Q on an active run asks y/n rather than quitting outright

	sse            *sseSubscription
	pushEventCount int // envelopes received via SSE so far — this package's own tests use it as proof of push, not poll (see sse_test.go)

	home         homeState
	conversation conversationState
	runInspector runInspectorState
	logs         logsState
	inbox        inboxState
	runners      runnersState
	benchmark    benchmarkState
	decision     decisionState

	quitting bool
}

// New constructs the root Model. onboarded=false triggers the first-run
// screen (entry.go decides this from whether $KAIROS_HOME/.onboarded
// exists yet).
func New(ctx context.Context, client *cli.Client, homePath string, onboarded bool) Model {
	screen := ScreenHome
	if !onboarded {
		screen = ScreenOnboarding
	}
	return Model{
		client:    client,
		ctx:       ctx,
		homePath:  homePath,
		screen:    screen,
		mode:      ModeNAV,
		onboarded: onboarded,
		sse:       newSSESubscription(ctx, client),
	}
}

// Init kicks off the first fetch for whatever screen is showing plus the
// live SSE subscription's read loop (waitForSSE) — there is no tea.Tick
// here. Every subsequent screen update is triggered by an sseEventMsg
// arriving (Update, below), not by a timer.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.refreshCmd(), waitForSSE(m.sse.ch))
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case sseEventMsg:
		// Push, not poll: refetch whatever the current screen needs the
		// instant an event lands, then keep listening for the next one.
		// Unfiltered by design (every screen's refetch is already cheap
		// and this is one local daemon's event volume — AGENTS.md's "three
		// orders of magnitude below where it would matter"), so no
		// per-stream routing logic is needed to get correctness right.
		m.pushEventCount++
		return m, tea.Batch(m.refreshCmd(), waitForSSE(m.sse.ch))

	case sseStatusMsg:
		switch msg.status {
		case cli.FollowDisconnected:
			m.statusLine = "live updates: reconnecting..."
		case cli.FollowConnected:
			m.statusLine = ""
		}
		return m, waitForSSE(m.sse.ch)

	case tea.KeyMsg:
		return m.handleKey(msg)

	case runsFetchedMsg:
		m.home.runs = msg.runs
		m.home.err = msg.err
		return m, nil

	case runStateFetchedMsg:
		return m.applyRunStateFetched(msg)

	case conversationFetchedMsg:
		m.conversation.messages = msg.messages
		m.conversation.err = msg.err
		return m, nil

	case inboxFetchedMsg:
		m.inbox.items = msg.items
		m.inbox.err = msg.err
		return m, nil

	case decisionContextFetchedMsg:
		return m.applyDecisionContextFetched(msg)

	case doctorFetchedMsg:
		m.runners.doctor = msg.resp
		m.runners.doctorErr = msg.err
		return m, nil

	case approveResultMsg:
		return m.applyApproveResult(msg)

	case benchmarkFetchedMsg:
		return m.applyBenchmarkFetched(msg), nil

	case conversationSentMsg:
		if msg.err != nil {
			m.statusLine = "send failed: " + msg.err.Error()
			return m, nil
		}
		return m, m.fetchConversation(m.conversation.runID)

	case doResultMsg:
		if msg.err != nil {
			m.statusLine = "kairos do failed: " + msg.err.Error()
			return m, nil
		}
		m.navigate(ScreenConversation)
		m.conversation.runID = msg.resp.ConversationRunID
		m.conversation.isAdHoc = true
		m.statusLine = "started " + msg.resp.RunID
		return m, m.fetchConversation(msg.resp.ConversationRunID)
	}

	return m, nil
}

func (m Model) View() string {
	if m.quitting {
		return "detached. engine still running.\n"
	}
	if m.paletteOpen {
		return ": " + m.paletteInput + "█\n\n(paste a run id, a screen name, or a verb — enter to go, esc to cancel)\n"
	}
	switch m.screen {
	case ScreenOnboarding:
		return m.viewOnboarding()
	case ScreenHome:
		return m.viewHome()
	case ScreenConversation:
		return m.viewConversation()
	case ScreenRunInspector:
		return m.viewRunInspector()
	case ScreenLogs:
		return m.viewLogs()
	case ScreenInbox:
		return m.viewInbox()
	case ScreenRunners:
		return m.viewRunners()
	case ScreenBenchmark:
		return m.viewBenchmark()
	case ScreenDecision:
		return m.viewDecision()
	}
	return ""
}

// navigate moves to screen, recording the current one so esc can walk back
// — 09-cli-and-tui.md's "one level deep everywhere" navigation graph.
func (m *Model) navigate(to Screen) {
	m.prevScreen = m.screen
	m.screen = to
	m.mode = ModeNAV
}

func (m *Model) back() {
	m.screen = m.prevScreen
	m.mode = ModeNAV
}

func withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 10*time.Second)
}
