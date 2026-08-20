package tui

import (
	"fmt"
	"strings"
)

// minDecisionWidth/minDecisionHeight are 09-cli-and-tui.md's literal
// 80x24 floor for the decision screen — "the only place in the design
// where a UI refuses to do its job on principle."
const (
	minDecisionWidth  = 80
	minDecisionHeight = 24
)

func (m Model) decisionScreenTooSmall() bool {
	return m.width > 0 && m.height > 0 && (m.width < minDecisionWidth || m.height < minDecisionHeight)
}

func (m Model) viewDecision() string {
	if m.decisionScreenTooSmall() {
		return m.viewDecisionRefusal()
	}

	var b strings.Builder
	fmt.Fprintf(&b, "DECISION · %s / %s\n\n", m.decision.runID, m.decision.nodeID)

	if m.decision.evidenceErr != nil {
		fmt.Fprintf(&b, "evidence failed to load: %v\n\n  R retry   esc back\n", m.decision.evidenceErr)
		return b.String()
	}

	c := m.decision.ctx

	b.WriteString(paneHeader("RISK", m.decision.focus == paneRisk, m.decision.viewed[paneRisk]))
	if c.Irreversible {
		fmt.Fprintf(&b, "  ⚠ irreversible: %s\n", c.Effect)
	} else {
		b.WriteString("  ✓ no irreversible effect recorded\n")
	}
	passed, total := 0, len(c.Gates)
	for _, g := range c.Gates {
		if g.Passed {
			passed++
		}
	}
	fmt.Fprintf(&b, "  gates %d/%d\n", passed, total)
	fmt.Fprintf(&b, "  attempts %d\n\n", c.Attempts)

	b.WriteString(paneHeader("HOST EFFECTS", m.decision.focus == paneHostEffects, m.decision.viewed[paneHostEffects]))
	if c.Effect != "" {
		fmt.Fprintf(&b, "  %s\n\n", c.Effect)
	} else {
		b.WriteString("  none\n\n")
	}

	b.WriteString(paneHeader("CHANGED", m.decision.focus == paneChanged, m.decision.viewed[paneChanged]))
	b.WriteString("  — (no diff data exposed by the daemon API yet; see kairos diff)\n\n")

	b.WriteString(paneHeader("FINDINGS", m.decision.focus == paneFindings, m.decision.viewed[paneFindings]))
	if len(c.Findings) == 0 {
		b.WriteString("  none\n")
	}
	for _, f := range c.Findings {
		mark := " "
		if f.Severity == "high" || f.Severity == "critical" {
			if m.decision.riskAccepted[f.ID] {
				mark = "✓"
			} else {
				mark = "⚠"
			}
		}
		fmt.Fprintf(&b, "  %s %s  %s  %s\n", mark, f.Severity, f.ID, f.Message)
	}
	if len(c.hasHighOrCriticalFindings()) > 0 {
		b.WriteString("  r  accept risk for the next unaccepted high/critical finding\n")
	}
	b.WriteString("\n")

	b.WriteString(paneHeader("YOUR DECISION", m.decision.focus == paneDecision, m.decision.viewed[paneDecision]))
	if !m.decisionReadyForFocus() {
		b.WriteString("  (unreachable — walk every pane above with tab, and accept any listed risk)\n")
	} else {
		b.WriteString("  1 approve   2 request-changes   3 reject\n")
		fmt.Fprintf(&b, "  choice: %s\n", orDash(m.decision.decisionChoice))
		fmt.Fprintf(&b, "  reason (r to edit): %s\n", orDash(m.decision.reasonInput))
		fmt.Fprintf(&b, "  type %q to confirm (t to edit): %s\n", orDash(m.decision.decisionChoice), orDash(m.decision.typedInput))
		if m.decision.submitErr != nil {
			fmt.Fprintf(&b, "  submit failed: %v\n", m.decision.submitErr)
		}
		b.WriteString("  enter  submit\n")
	}

	if m.statusLine != "" {
		fmt.Fprintf(&b, "\n%s\n", m.statusLine)
	}
	b.WriteString("\ntab next pane · esc back\n")
	return b.String()
}

func (m Model) viewDecisionRefusal() string {
	return fmt.Sprintf(
		"This terminal is %dx%d. The decision screen needs %dx%d to show the\n"+
			"risk summary, the host effects, and the controls at once.\n\n"+
			"A cramped decision screen is how gates become theatre, so this refuses\n"+
			"rather than truncating.\n\n"+
			"  widen the terminal · o open in the browser (not yet available — see kairos show) · kairos show %s\n",
		m.width, m.height, minDecisionWidth, minDecisionHeight, m.decision.runID)
}

func paneHeader(name string, focused, viewed bool) string {
	mark := " "
	if focused {
		mark = "▸"
	} else if viewed {
		mark = "✓"
	}
	return fmt.Sprintf("%s %s\n", mark, name)
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
