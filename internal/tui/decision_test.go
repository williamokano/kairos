package tui

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func key(runes string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(runes)}
}

func namedKey(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

func newDecisionTestModel() Model {
	m := New(context.Background(), nil, "", true)
	m.width, m.height = 100, 40
	m.navigate(ScreenDecision)
	m.decision = decisionState{
		runID: "run1", nodeID: "approve", execID: "approve#a1.i1",
		focus:        paneRisk,
		riskAccepted: map[string]bool{},
		ctx: decisionContext{
			Findings: []findingSummary{{ID: "f1", Message: "sql injection risk", Severity: "high"}},
		},
	}
	return m
}

func TestDecision_focusOrderEnforced(t *testing.T) {
	m := newDecisionTestModel()

	if m.decisionReadyForFocus() {
		t.Fatal("decision pane must not be reachable before any evidence pane has been viewed")
	}

	// Walk risk -> hostEffects -> changed without accepting the high
	// finding yet: tab three times should stop just before findings, since
	// paneFindings itself must also be *viewed* before decision opens.
	nm, _ := m.handleDecisionKey(namedKey(tea.KeyTab))
	m = nm
	if m.decision.focus != paneHostEffects {
		t.Fatalf("focus = %v, want paneHostEffects after one tab", m.decision.focus)
	}
	nm, _ = m.handleDecisionKey(namedKey(tea.KeyTab))
	m = nm
	nm, _ = m.handleDecisionKey(namedKey(tea.KeyTab))
	m = nm
	if m.decision.focus != paneFindings {
		t.Fatalf("focus = %v, want paneFindings after three tabs", m.decision.focus)
	}

	// Attempting to advance past findings without accepting the high
	// finding's risk must refuse — focus stays on findings.
	nm, _ = m.handleDecisionKey(namedKey(tea.KeyTab))
	m = nm
	if m.decision.focus == paneDecision {
		t.Fatal("decision pane became reachable without the high finding's risk being accepted")
	}

	// Accept the risk, then tab should finally reach the decision pane.
	nm, _ = m.handleDecisionKey(key("r"))
	m = nm
	if !m.decision.riskAccepted["f1"] {
		t.Fatal("expected 'r' on the findings pane to accept the high finding's risk")
	}
	nm, _ = m.handleDecisionKey(namedKey(tea.KeyTab))
	m = nm
	if m.decision.focus != paneDecision {
		t.Fatalf("focus = %v, want paneDecision once every prior pane is viewed and risk accepted", m.decision.focus)
	}
}

func TestDecision_typedWordRequired(t *testing.T) {
	m := newDecisionTestModel()
	// Force every precondition satisfied except the typed word.
	for p := paneRisk; p < paneDecision; p++ {
		m.decision.viewed[p] = true
	}
	m.decision.riskAccepted["f1"] = true
	m.decision.focus = paneDecision
	m.decision.decisionChoice = "approve"
	m.decision.reasonInput = "looks fine"
	m.decision.typedInput = "approv" // deliberately short — not an exact match

	nm, cmd := m.handleDecisionKey(namedKey(tea.KeyEnter))
	m = nm
	if cmd != nil {
		t.Fatal("expected no submit command when the typed word doesn't exactly match the decision")
	}
	if m.statusLine == "" {
		t.Fatal("expected a status line explaining the mismatch")
	}

	m.decision.typedInput = "approve"
	_, cmd = m.handleDecisionKey(namedKey(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("expected a submit command once the typed word exactly matches")
	}
}

func TestDecision_singleKeyNeverApproves(t *testing.T) {
	m := newDecisionTestModel()
	for p := paneRisk; p < paneDecision; p++ {
		m.decision.viewed[p] = true
	}
	m.decision.riskAccepted["f1"] = true
	m.decision.focus = paneDecision

	// A muscle-memory 'y' on the decision pane must do nothing — there is
	// no key anywhere on this screen that submits in one press.
	nm, cmd := m.handleDecisionKey(key("y"))
	m = nm
	if cmd != nil || m.decision.decisionChoice != "" {
		t.Fatal("a single keypress ('y') must never approve a decision")
	}
}

func TestDecision_evidenceLoadFailureBlocksForm(t *testing.T) {
	m := newDecisionTestModel()
	m.decision.evidenceErr = context.DeadlineExceeded

	// Any key other than R (retry) or esc must be a no-op while evidence
	// failed to load — the form is blocked, not merely degraded.
	nm, cmd := m.handleDecisionKey(namedKey(tea.KeyTab))
	m = nm
	if cmd != nil || m.decision.focus != paneRisk {
		t.Fatal("tab must do nothing while evidence failed to load")
	}
	nm, _ = m.handleDecisionKey(key("1"))
	m = nm
	if m.decision.decisionChoice != "" {
		t.Fatal("selecting a decision must be impossible while evidence failed to load")
	}

	_, cmd = m.handleDecisionKey(key("R"))
	if cmd == nil {
		t.Fatal("expected R (retry) to issue a re-fetch command")
	}
}

func TestDecision_riskAcceptanceIsSeparateFromDecisionControl(t *testing.T) {
	m := newDecisionTestModel()
	for p := paneRisk; p < paneDecision; p++ {
		m.decision.viewed[p] = true
	}
	m.decision.focus = paneDecision
	// Deliberately did NOT accept the high finding's risk.
	if m.decisionReadyForFocus() {
		t.Fatal("reaching the decision pane must still require risk acceptance, independent of pane-viewed state")
	}
}

func Test80x24Refusal(t *testing.T) {
	m := newDecisionTestModel()

	m.width, m.height = 100, 40
	if m.decisionScreenTooSmall() {
		t.Fatal("a comfortably large terminal must not trigger the refusal")
	}

	m.width, m.height = 68, 18
	if !m.decisionScreenTooSmall() {
		t.Fatal("a 68x18 terminal must trigger the decision screen's refusal")
	}
	view := m.viewDecision()
	if view == "" {
		t.Fatal("expected the refusal view to render something, not an empty frame")
	}
}
