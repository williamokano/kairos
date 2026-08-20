package tui

import "github.com/williamokano/kairos/internal/cli"

type homeState struct {
	runs    []cli.RunSummary
	cursor  int
	err     error
	compose string // INPUT-mode composer text, "kairos do"-shaped (fetch/dispatch not wired — see Future work)
}

type conversationState struct {
	runID    string
	messages []cli.ConversationMessage
	err      error
	reply    string
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
