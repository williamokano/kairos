package registry

import (
	"fmt"
	"os"

	"sigs.k8s.io/yaml"
)

func boolPtr(b bool) *bool { return &b }

// BaselineGates is "kairos/baseline" — compiled into the binary,
// cannot be removed, only extended (05-gates.md: "The constitution").
// Trimmed to what this document's implemented gate kinds can express:
// guardrails-untouched (git-diff) and no-secrets (regex over
// added-lines). clean-tree is also baseline, but is auto-attached only
// to workspace: read nodes rather than every node — see
// MandatoryBaselineGateIDs vs. CleanTreeGateID.
var BaselineGates = map[string]GateDef{
	"guardrails-untouched": {
		ID: "guardrails-untouched", Kind: GateGitDiff, Waivable: false,
		Message: "a change touched a guardrail path",
		GitDiffPathsForbidden: []string{
			".kairos/**", ".github/**", ".git/hooks/**",
		},
	},
	"no-secrets": {
		ID: "no-secrets", Kind: GateRegex, Waivable: false,
		Message:     "an added line looks like it contains a credential",
		RegexOver:   "added-lines",
		RegexAbsent: `(?i)(api[_-]?key|secret[_-]?key|password\s*=\s*['"]|-----BEGIN [A-Z ]*PRIVATE KEY-----)`,
	},
	"clean-tree": {
		ID: "clean-tree", Kind: GateGitDiff, Waivable: false,
		Message:       "a read-only node modified the working tree",
		GitDiffDirty:  boolPtr(false),
		GitDiffStaged: boolPtr(false),
	},
}

// MandatoryBaselineGateIDs names 05-gates.md's `mandatoryGates` clause —
// the IDs that clause would merge into every workflow, non-removably.
// This document does NOT auto-attach them to every node's own Gates
// list: most existing nodes across L05-L10 have no git workspace and no
// configured BaseRef, so unconditionally forcing a git-diff/regex gate
// onto every node would fail those gates for reasons that have nothing
// to do with the workflow author's intent, on every run in the system —
// a regression far larger than this document's actual scope. Mandatory,
// non-removable auto-attachment (05-gates.md's stage-keyed YAML
// mechanism, not implemented here at all) is Future work, gated on a
// project actually having a git workspace and BaseRef configured; see
// L11-policy-secrets.md's Documented decisions. Today, BaselineGates are
// merely NAME-RESOLVABLE: a workflow author who explicitly lists
// "guardrails-untouched" in a node's own Gates []string gets the real
// baseline definition, but nothing is forced onto a node that didn't ask
// for it.
var MandatoryBaselineGateIDs = []string{"guardrails-untouched", "no-secrets"}

// CleanTreeGateID names 05-gates.md's built-in clean-tree gate — see
// MandatoryBaselineGateIDs's doc comment for why this document resolves
// it by name only rather than auto-attaching it to workspace: read
// nodes.
const CleanTreeGateID = "clean-tree"

// LoadConstitutionGates reads one constitution.yaml's top-level `gates:`
// block (the same shorthand parseGates already understands for a
// workflow's own inline gates:). A missing file returns an empty,
// non-nil map and no error — an absent project or repo constitution file
// is normal, not a publish failure. The raw file bytes are also
// returned, for hash-pinning (see MergeConstitution).
func LoadConstitutionGates(path string) (map[string]GateDef, []byte, error) {
	if path == "" {
		return map[string]GateDef{}, nil, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]GateDef{}, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("reading constitution %s: %w", path, err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("parsing constitution %s: %w", path, err)
	}
	gates, err := parseGates(doc)
	if err != nil {
		return nil, nil, fmt.Errorf("constitution %s: %w", path, err)
	}
	return gates, data, nil
}

// MergeConstitution resolves 05-gates.md's three-layer stack:
// kairos/baseline (lowest precedence) -> repo-level (merged in) ->
// project-level (authoritative, highest precedence — the doc's own word
// for it). A gate ID defined at more than one layer takes the
// highest-precedence layer's definition; baseline's three built-ins are
// never removable, only shadowed by a same-named override (matching
// "cannot be removed, only extended"). Returns the merged gate library
// and the repo-level file's raw bytes (nil if absent) for the caller to
// hash-pin.
func MergeConstitution(repoPath, projectPath string) (map[string]GateDef, []byte, error) {
	merged := map[string]GateDef{}
	for id, gd := range BaselineGates {
		merged[id] = gd
	}
	repoGates, repoBytes, err := LoadConstitutionGates(repoPath)
	if err != nil {
		return nil, nil, err
	}
	for id, gd := range repoGates {
		merged[id] = gd
	}
	projectGates, _, err := LoadConstitutionGates(projectPath)
	if err != nil {
		return nil, nil, err
	}
	for id, gd := range projectGates {
		merged[id] = gd
	}
	return merged, repoBytes, nil
}

// LoadWithConstitution wraps Load, merging the resolved constitution
// (baseline + repoPath + projectPath, either of which may be empty) into
// the returned Definition's Gates library so a node's own Gates []string
// can resolve a baseline/project/repo gate ID by name, in addition to the
// workflow's own inline gates: block. It does NOT attach any gate ID to
// a node that did not already declare it — see MandatoryBaselineGateIDs's
// doc comment for why automatic, non-removable attachment is Future work
// rather than built here. A workflow's own inline `gates:` block (already
// in def.Gates from Load) takes precedence over the constitution for any
// overlapping ID — it is the most specific layer, declared right where
// it is used.
func LoadWithConstitution(path, repoPath, projectPath string) (Definition, error) {
	def, err := Load(path)
	if err != nil {
		return Definition{}, err
	}
	merged, _, err := MergeConstitution(repoPath, projectPath)
	if err != nil {
		return Definition{}, fmt.Errorf("resolving constitution: %w", err)
	}
	for id, gd := range def.Gates {
		merged[id] = gd
	}
	def.Gates = merged
	return def, nil
}
