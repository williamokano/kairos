package registry

import (
	"fmt"
	"strings"
)

// deletedFields is 03-workflows.md's "what died from the spec" table: keys
// that must be a hard publish error, not silently ignored (AGENTS §4 rule
// 1: no silent failure — a workflow author porting an old spec deserves an
// error, not silent data loss).
var deletedFields = []string{"worker", "runtime", "image", "features", "pool", "ttl", "holdThreshold"}

var deletedResourceFields = []string{"cpu", "memory", "disk"}

// Validate runs the publish validator: the denylist walk over doc's raw
// map (workflow- and node-level), and the structural checks over the
// defaulted def (output schema presence, onTimeout-when-wait, node ID
// rules, and the static input-reference subset — see Documented decisions
// #2 in L03-definition-validator.md for exactly what that subset covers
// and does not).
func Validate(doc rawDoc, def Definition) error {
	if err := checkDenylist(doc.raw, "workflow"); err != nil {
		return err
	}
	for _, nm := range doc.nodeMaps() {
		id, _ := nm["id"].(string)
		if err := checkDenylist(nm, fmt.Sprintf("node %q", id)); err != nil {
			return err
		}
	}

	if def.Name == "" {
		return fmt.Errorf("workflow: name is required")
	}
	if len(def.Nodes) == 0 {
		return fmt.Errorf("workflow: at least one node is required")
	}

	seen := map[string]bool{}
	for i, nd := range def.Nodes {
		if nd.ID == "" {
			return fmt.Errorf("node[%d]: id is required", i)
		}
		if nd.ID == "$succeed" || nd.ID == "$fail" {
			return fmt.Errorf("node %q: id must not be a reserved sink name", nd.ID)
		}
		if seen[string(nd.ID)] {
			return fmt.Errorf("node %q: duplicate node id", nd.ID)
		}
		seen[string(nd.ID)] = true

		if nd.Actor == "" {
			return fmt.Errorf("node %q: actor is required (no default — see Documented decisions #5)", nd.ID)
		}

		if nd.OutputSchema == nil && requiresOutputSchema(nd.Actor) {
			return fmt.Errorf("node %q: output or outputSchema is required for actor %q", nd.ID, nd.Actor)
		}

		if nd.Wait != nil {
			if nd.Wait.OnTimeout != "escalate" && nd.Wait.OnTimeout != "park" {
				return fmt.Errorf("node %q: wait.onTimeout is required and must be \"escalate\" or \"park\"", nd.ID)
			}
			if len(nd.Wait.On) == 0 {
				return fmt.Errorf("node %q: wait.on must name at least one source", nd.ID)
			}
		}

		switch nd.RestartPolicy {
		case RestartRerun, RestartFailToHuman, RestartAdopt:
			// fine — L06's reconciliation loop now implements adoption
			// (internal/engine/reconcile.go's VerdictAlive branch).
		default:
			return fmt.Errorf("node %q: unknown restartPolicy %q", nd.ID, nd.RestartPolicy)
		}

		if err := checkInputRefs(def.Nodes, i); err != nil {
			return err
		}

		// 05-gates.md's judged gate kind, four rules that don't bend:
		// "the judge is never the session under judgement" — publish-time
		// validation rejects a judged constraint whose actor equals the
		// actor of any node it gates. Checked against this document's own
		// gates: map only (the constitution's baseline/project/repo
		// layers are resolved later, at dispatch time, by L11's
		// registry.LoadWithConstitution — a workflow author cannot smuggle
		// a self-judging gate past THIS check via those layers, but this
		// check also cannot see them; see L11-policy-secrets.md's
		// Documented decisions for why that gap is accepted here).
		for _, gateID := range nd.Gates {
			if gd, ok := def.Gates[gateID]; ok && gd.Kind == GateJudged {
				for _, judgeActor := range gd.JudgeActors {
					if judgeActor == nd.Actor {
						return fmt.Errorf("node %q: judged gate %q names actor %q as a judge, which is also this node's own actor — the judge is never the session under judgement", nd.ID, gateID, judgeActor)
					}
				}
			}
		}

		// A node's Gates []string is NOT required to resolve against this
		// document's own top-level gates: map — 05-gates.md's real
		// resolution merges kairos/baseline (compiled-in) with a project
		// constitution outside every workspace and a repo-level file
		// (L11 scope, not built yet). Erroring on every unresolved name
		// today would reject 03-workflows.md's own canonical example
		// (gates: [build, lint, no-todos, no-secrets, guardrails-untouched]),
		// none of which this narrow slice defines. internal/engine's
		// evaluateGates instead WARN-logs and skips a gate ID with no
		// local definition — see L10-constraints-gates.md's Documented
		// decisions.
	}

	for id, gd := range def.Gates {
		if gd.Kind == GateCommand && len(gd.Workdir) > 0 && gd.Workdir[0] == '/' {
			return fmt.Errorf("gates.%s: workdir must be relative to the workspace — absolute paths are rejected (05-gates.md)", id)
		}
	}

	return nil
}

func checkDenylist(m raw, scope string) error {
	for _, k := range deletedFields {
		if _, ok := m[k]; ok {
			return fmt.Errorf("%s: field %q was removed from the spec (03-workflows.md \"what died from the spec\")", scope, k)
		}
	}
	if net, ok := m["network"].(map[string]any); ok {
		if _, ok := net["egress"]; ok {
			return fmt.Errorf("%s: network.egress is not supported — the local executor enforces no egress control", scope)
		}
		if _, ok := net["allow"]; ok {
			return fmt.Errorf("%s: network.allow is not supported — the local executor enforces no egress control", scope)
		}
	}
	if res, ok := m["resources"].(map[string]any); ok {
		for _, k := range deletedResourceFields {
			if _, ok := res[k]; ok {
				return fmt.Errorf("%s: field \"resources.%s\" was removed from the spec (03-workflows.md \"what died from the spec\")", scope, k)
			}
		}
	}
	return nil
}

// checkInputRefs is the static input-reference validator subset (decision
// #2): a "$.outputs.<nodeID>..." selector must name a node earlier in
// document order. It does not check whether that node's output schema
// actually contains the referenced field, and it does not check
// "$.params.*"/"$.artifacts.*" selectors at all — deferred, documented.
func checkInputRefs(nodes []NodeDef, idx int) error {
	nd := nodes[idx]
	earlier := map[string]bool{}
	for _, n := range nodes[:idx] {
		earlier[string(n.ID)] = true
	}
	for key, ref := range nd.Inputs {
		if !strings.HasPrefix(ref.Path, "$.outputs.") {
			continue
		}
		rest := strings.TrimPrefix(ref.Path, "$.outputs.")
		nodeID, _, _ := strings.Cut(rest, ".")
		if nodeID == "" || !earlier[nodeID] {
			return fmt.Errorf("node %q: inputs.%s references %q, which is not an earlier node in this workflow", nd.ID, key, nodeID)
		}
	}
	return nil
}
