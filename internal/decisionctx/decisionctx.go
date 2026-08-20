// Package decisionctx computes a Waiting(human) node's decision evidence —
// risk, gate verdicts, findings, attempt count — from a run's real event
// log, never from model-authored text taken at face value
// (09-cli-and-tui.md: "the risk summary is computed, never authored by a
// model"). It exists so the TUI (L15) and the web UI (L20) render the
// identical evidence from the identical computation — 10-webui.md: "the
// decision card is identical in both, rendered from the same
// server-computed context" — rather than each surface growing its own,
// silently-diverging notion of risk.
package decisionctx

import (
	"encoding/json"

	"github.com/williamokano/kairos/internal/cli"
)

// Context is the evidence a decision screen renders.
type Context struct {
	Effect       string // from EffectConfirmationRequested/Parked, empty if this is a plain wait:human node
	Irreversible bool
	Gates        []GateVerdict
	Findings     []FindingSummary
	Attempts     int
}

type GateVerdict struct {
	GateID string
	Passed bool
	Reason string
}

type FindingSummary struct {
	ID       string
	Message  string
	Severity string
}

// HighOrCritical returns the findings whose severity requires a separate
// risk-acceptance control before a decision pane may be reached
// (09-cli-and-tui.md's anti-rubber-stamp rule).
func (c Context) HighOrCritical() []FindingSummary {
	var out []FindingSummary
	for _, f := range c.Findings {
		if f.Severity == "high" || f.Severity == "critical" {
			out = append(out, f)
		}
	}
	return out
}

// Build folds a run's real events into the evidence one node/exec's
// decision needs — gate verdicts, findings, the effect (if any) awaiting
// confirmation, and the attempt count.
func Build(envs []cli.Envelope, nodeID string) Context {
	var c Context
	maxAttempt := 0
	for _, env := range envs {
		switch env.EventType {
		case "constraint.evaluated":
			var p struct {
				NodeID, GateID, Reason string
				Passed                 bool
			}
			if json.Unmarshal(env.Event, &p) == nil && p.NodeID == nodeID {
				c.Gates = append(c.Gates, GateVerdict{GateID: p.GateID, Passed: p.Passed, Reason: p.Reason})
			}
		case "node.gates.evaluated":
			var p struct {
				NodeID   string
				Findings []FindingSummary
			}
			if json.Unmarshal(env.Event, &p) == nil && p.NodeID == nodeID {
				c.Findings = append(c.Findings, p.Findings...)
			}
		case "effect.confirmation.requested", "effect.confirmation.parked":
			var p struct{ NodeID, Effect string }
			if json.Unmarshal(env.Event, &p) == nil && p.NodeID == nodeID {
				c.Effect = p.Effect
				c.Irreversible = IsIrreversibleEffect(p.Effect)
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

// IsIrreversibleEffect is a narrow, honest heuristic over the effect's own
// declared name (e.g. "git.push", "gh.pr.create") — real gate/effect risk
// classification (a full reversibility taxonomy) does not exist yet; this
// flags the two builtins internal/effect (L12) actually implements.
func IsIrreversibleEffect(effect string) bool {
	switch effect {
	case "git.push", "gh.pr.create":
		return true
	}
	return effect != ""
}
