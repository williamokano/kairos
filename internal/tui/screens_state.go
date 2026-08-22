package tui

import "github.com/williamokano/kairos/internal/cli"

type homeState struct {
	runs    []cli.RunSummary
	cursor  int
	err     error
	compose string // INPUT-mode composer text — submitComposer (keys.go) sends it via kairos do
}

type conversationState struct {
	runID    string
	messages []cli.ConversationMessage
	err      error
	reply    string
	// isAdHoc is true only when this Conversation was reached via kairos
	// do (submitComposer -> doResultMsg) — a real wait: conversation
	// workflow's reply box (handleConversationInputKey) posts a plain
	// message and waits for the SAME node to resolve; an ad hoc chat's
	// run is already terminal after turn one, so its reply box instead
	// calls Do(text, continueRunId: runID) for a real new LLM turn with
	// native session resume (see internal/api/do.go's handler doc
	// comment). Reset to false on every OTHER path into this screen
	// (screens_runinspector.go's 'c' key) so a stale true never survives
	// into an unrelated, real conversation.
	isAdHoc bool
}

type runInspectorState struct {
	runID string
	state cli.RunState
	err   error
}

// logsState is intentionally thin: no log-streaming API endpoint exists
// yet (see screens_logs.go), so there is nothing to cache here besides
// which run is focused.
type logsState struct {
	runID string
}

type inboxState struct {
	items  []inboxItem
	cursor int
	err    error
}

type runnersState struct {
	doctor    cli.DoctorResponse
	doctorErr error
}

type benchmarkState struct {
	input  string // INPUT-mode text: "<runA> <runB>"
	result cli.CompareResult
	err    error
	have   bool
}

// decisionPane is one of the five evidence panes plus the decision control,
// in the fixed, non-negotiable order 09-cli-and-tui.md requires:
// objective(risk) -> risk -> host effects -> changed -> findings -> decision.
// objective has no pane of its own (it is a header line), so the walk is
// risk -> hostEffects -> changed -> findings -> decision.
type decisionPane int

const (
	paneRisk decisionPane = iota
	paneHostEffects
	paneChanged
	paneFindings
	paneDecision
	numPanes
)

// decisionState is the anti-rubber-stamp screen's full state. viewed tracks,
// per pane, whether it has actually been rendered at least once — the
// decision pane's focus is computed from this, not from a simple counter,
// so "has this pane been shown" is a real fact rather than assumed from
// tab-count.
type decisionState struct {
	runID, nodeID, execID string

	ctx         decisionContext
	evidenceErr error

	focus  decisionPane
	viewed [numPanes]bool

	riskAccepted map[string]bool // finding ID -> accepted, for high/critical findings' separate control

	decisionChoice string // "approve" | "request-changes" | "reject", chosen via the decision control
	reasonInput    string
	typedInput     string // must equal decisionChoice exactly to submit — the typed-word requirement
	activeField    decisionField

	submitting bool
	submitErr  error
	answered   bool
}

// dirPickerState is the directory-tree picker's own state — the TUI's
// answer to internal/web/fsbrowse.go's picker on the web Projects page,
// against the SAME GET /fs/browse endpoint (cli.Client.BrowseFS), not a
// parallel filesystem-walking implementation.
type dirPickerState struct {
	resp   cli.FSBrowseResponse
	cursor int
	err    error
}

type projectsState struct {
	projects []cli.Project
	cursor   int
	err      error

	creating  bool
	name      string
	picking   bool // true once name is entered and the directory picker is active
	picker    dirPickerState
	createErr error
}

type sessionsState struct {
	sessions []cli.Session
	cursor   int
	err      error

	starting     bool
	startField   int // 0 = project name, 1 = actor
	startProject string
	startActor   string
	startErr     error
}

// sessionChatState is the TUI's real answer to the web UI's
// /sessions/{id} page: the session id lives here, in the screen's own
// state, for the screen's entire lifetime — it is set once on entry
// (navigateToSessionChat) and never re-derived from a value that could be
// dropped in transit (the exact bug class the web UI's fragile two-step
// /chat?session= picker had — see L26-session-chat.md). Every message
// this screen sends reads m.sessionChat.sessionID directly, structurally
// incapable of "forgetting" it.
type sessionChatState struct {
	sessionID string
	session   cli.Session
	messages  []cli.ConversationMessage
	err       error
	reply     string

	ending     bool
	endStep    int // 0 = reason, 1 = typed confirm (must equal sessionID)
	endReason  string
	endConfirm string
	endErr     error
}
