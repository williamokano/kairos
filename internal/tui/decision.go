package tui

import (
	"encoding/json"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/williamokano/kairos/internal/cli"
)

// defaultEventsReadTimeout bounds how long fetchDecisionContext waits for a
// run's historical event replay to finish (internal/cli.Client.Events).
const defaultEventsReadTimeout = 3 * time.Second

// decisionContext is the evidence a decision screen renders, assembled by
// scanning the run's real event log (internal/cli.Client.Events) — there is
// no summarized "decision context" endpoint yet, so this is computed here,
// from real recorded facts, never model-authored text taken at face value
// (09-cli-and-tui.md: "the risk summary is computed, never authored by a
// model"). See L15-tui.md's Documented decisions for exactly which of the
// mockup's richer fields (files-outside-workspace, network egress list,
// session compaction count) have no backing data yet and are honestly
// omitted rather than fabricated.
type decisionContext struct {
	Effect       string // from EffectConfirmationRequested/Parked, empty if this is a plain wait:human node
	Irreversible bool
	Gates        []gateVerdict
	Findings     []findingSummary
	Attempts     int
}

type gateVerdict struct {
	GateID string
	Passed bool
	Reason string
}

type findingSummary struct {
	ID       string
	Message  string
	Severity string
}

func (c decisionContext) hasHighOrCriticalFindings() []findingSummary {
	var out []findingSummary
	for _, f := range c.Findings {
		if f.Severity == "high" || f.Severity == "critical" {
			out = append(out, f)
		}
	}
	return out
}

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
		return decisionContextFetchedMsg{runID: runID, nodeID: nodeID, execID: execID, ctx: buildDecisionContext(envs, nodeID, execID)}
	}
}

// buildDecisionContext folds a run's real events into the evidence a
// decision needs — gate verdicts, findings, the effect (if any) awaiting
// confirmation, and the attempt count — for the one node/exec in question.
func buildDecisionContext(envs []cli.Envelope, nodeID, execID string) decisionContext {
	var c decisionContext
	maxAttempt := 0
	for _, env := range envs {
		switch env.EventType {
		case "constraint.evaluated":
			var p struct {
				NodeID, GateID, Reason string
				Passed                 bool
			}
			if json.Unmarshal(env.Event, &p) == nil && p.NodeID == nodeID {
				c.Gates = append(c.Gates, gateVerdict{GateID: p.GateID, Passed: p.Passed, Reason: p.Reason})
			}
		case "node.gates.evaluated":
			var p struct {
				NodeID   string
				Findings []findingSummary
			}
			if json.Unmarshal(env.Event, &p) == nil && p.NodeID == nodeID {
				c.Findings = append(c.Findings, p.Findings...)
			}
		case "effect.confirmation.requested", "effect.confirmation.parked":
			var p struct{ NodeID, Effect string }
			if json.Unmarshal(env.Event, &p) == nil && p.NodeID == nodeID {
				c.Effect = p.Effect
				c.Irreversible = isIrreversibleEffect(p.Effect)
			}
		case "node.execution.started":
			var p struct {
				NodeID  string
				Attempt int
			}
			if json.Unmarshal(env.Event, &p) == nil && p.NodeID == nodeID && p.Attempt > maxAttempt {
				maxAttempt = p.Attempt
			}
		}
	}
	c.Attempts = maxAttempt
	if c.Attempts == 0 {
		c.Attempts = 1
	}
	return c
}

// isIrreversibleEffect is a narrow, honest heuristic over the effect's own
// declared name (e.g. "git.push", "gh.pr.create") — real gate/effect risk
// classification (a full reversibility taxonomy) does not exist yet; this
// flags the two builtins internal/effect (L12) actually implements.
func isIrreversibleEffect(effect string) bool {
	switch effect {
	case "git.push", "gh.pr.create":
		return true
	}
	return effect != ""
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
