// Package policy implements 05-gates.md's effect-permission tiers: "may
// this actor cause this outward mutation?" — a question answered per
// declared effect name, independent of gate/constraint evaluation
// ("is this work acceptable?"). Three tiers, exhaustive: allow, confirm,
// deny. Absence of a matching rule is a denial (the doc's "default: deny"
// line) unless the policy document itself sets a different top-level
// default.
//
// Scope, documented in full in L11-policy-secrets.md's Documented
// decisions: this package answers the tier for one effect name. It does
// NOT implement path/match sub-pattern scoping (policy.yaml's
// `paths: ["!.kairos/**"]` / `match: "kairos/**"` clauses) — those refine
// a decision per invocation arguments, which requires wiring this package
// into the specific builtin effect call sites L12 (effects +
// compensation) owns. Here, a rule's Match/Paths fields are parsed and
// carried on the Decision but never filtered on — the tier for an effect
// name is uniform regardless of arguments. Registered as NL-34.
package policy

import (
	"fmt"
	"os"
	"strings"

	"sigs.k8s.io/yaml"
)

// Tier is one of the three exhaustive outcomes 05-gates.md names.
type Tier string

const (
	Allow   Tier = "allow"
	Confirm Tier = "confirm"
	Deny    Tier = "deny"
)

// EffectRule is one `effects.<name>:` entry in policy.yaml. Exactly one
// of Allow/Confirm/Deny is meaningfully set per entry — 05-gates.md's own
// examples never combine tiers on one rule, and Decide treats Deny as
// authoritative if somehow more than one is present ("deny always beats
// allow regardless of order").
type EffectRule struct {
	Allow   string   `json:"allow,omitempty"`
	Confirm string   `json:"confirm,omitempty"` // "each" | "once-per-run" | "once-per-session"
	Deny    string   `json:"deny,omitempty"`
	Match   string   `json:"match,omitempty"`
	Paths   []string `json:"paths,omitempty"`
	Reason  string   `json:"reason,omitempty"`
}

// Policy is one resolved ~/.kairos/policy.yaml.
type Policy struct {
	Default string                `json:"default,omitempty"` // "deny" is the only value 05-gates.md documents
	Effects map[string]EffectRule `json:"effects,omitempty"`
}

// Decision is Decide's answer for one effect name.
type Decision struct {
	Tier   Tier
	Reason string
}

// Load reads and parses a policy.yaml file. A missing file is not an
// error — it returns Default(), matching the doc's framing of policy.yaml
// as "shipped default" rather than a required file an operator must
// author before anything works.
func Load(path string) (Policy, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Default(), nil
	}
	if err != nil {
		return Policy{}, fmt.Errorf("policy: reading %s: %w", path, err)
	}
	var p Policy
	if err := yaml.Unmarshal(data, &p); err != nil {
		return Policy{}, fmt.Errorf("policy: parsing %s: %w", path, err)
	}
	if p.Default == "" {
		p.Default = "deny"
	}
	return p, nil
}

// Default is kairos/baseline's shipped policy — 05-gates.md's own
// example trimmed to the effects this codebase's builtins actually name
// so far (git.push, gh.pr.create/merge, waiver.grant): everything else
// falls through to Default's deny-by-absence.
func Default() Policy {
	return Policy{
		Default: "deny",
		Effects: map[string]EffectRule{
			"git.commit":        {Allow: "*"},
			"git.branch.create": {Allow: "*", Match: "kairos/**"},
			"fs.write":          {Allow: "*"},
			"gh.read":           {Allow: "*"},
			"model.invoke":      {Allow: "*"},
			"git.push":          {Confirm: "once-per-run"},
			"gh.pr.create":      {Confirm: "each"},
			"gh.pr.comment":     {Confirm: "once-per-run"},
			"git.push.force":    {Deny: "*", Reason: "Force-push destroys history. Do it yourself."},
			"gh.pr.merge":       {Deny: "*", Reason: "Agents propose; humans dispose."},
			"gh.workflow.edit":  {Deny: "*", Reason: "An agent that can edit CI can pass its own CI."},
			"gh.release.create": {Deny: "*", Reason: "Releasing is a human decision."},
			"terraform.apply":   {Deny: "*", Reason: "No infrastructure mutation."},
			"waiver.grant":      {Deny: "*", Reason: "A gate an agent can waive enforces nothing."},
		},
	}
}

// Decide answers the one question this package exists to answer: which
// tier applies to effect. An exact-name rule wins over a trailing-`.*`
// wildcard rule (e.g. "deploy.*" matches "deploy.aws" only when no
// "deploy.aws" entry exists); no match falls through to p.Default.
func (p Policy) Decide(effect string) Decision {
	if rule, ok := p.Effects[effect]; ok {
		return decideRule(rule, effect)
	}
	for name, rule := range p.Effects {
		if prefix, ok := strings.CutSuffix(name, ".*"); ok && strings.HasPrefix(effect, prefix+".") {
			return decideRule(rule, effect)
		}
	}
	if p.Default == "allow" {
		return Decision{Tier: Allow, Reason: "no policy rule matched; default is allow"}
	}
	return Decision{Tier: Deny, Reason: fmt.Sprintf("no policy rule for effect %q; absence of a grant is a denial", effect)}
}

func decideRule(rule EffectRule, effect string) Decision {
	// Deny always wins regardless of which other fields are also set —
	// 05-gates.md: "deny always beats allow regardless of order."
	if rule.Deny != "" {
		reason := rule.Reason
		if reason == "" {
			reason = fmt.Sprintf("effect %q is denied by policy", effect)
		}
		return Decision{Tier: Deny, Reason: reason}
	}
	if rule.Confirm != "" {
		return Decision{Tier: Confirm, Reason: fmt.Sprintf("effect %q requires confirmation (%s)", effect, rule.Confirm)}
	}
	if rule.Allow != "" {
		return Decision{Tier: Allow, Reason: fmt.Sprintf("effect %q is allowed by policy", effect)}
	}
	return Decision{Tier: Deny, Reason: fmt.Sprintf("effect %q has a rule with no tier set; treated as denied", effect)}
}
