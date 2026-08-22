package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FlowDefinitionInfo is one saved workflow file under $KAIROS_HOME/flows —
// the durable answer to "how do I create a workflow?": until now nothing
// in this system let a user author a NEW definition through any surface;
// a workflow could only be *referenced* by a path that already existed on
// disk (kairos run <file>, POST /runs's definitionPath). This is a real,
// hand-authorable counterpart to SynthesizeAdHoc's machine-authored one.
type FlowDefinitionInfo struct {
	Name string
	Path string
}

// flowNamePattern is deliberately narrow (no path separators, no leading
// dot) — Name becomes a filename component directly; a name containing
// "../" must never let SaveFlow escape $KAIROS_HOME/flows.
func validFlowName(name string) bool {
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return false
	}
	return true
}

// SaveFlow validates yamlText through the EXACT SAME Load path every
// hand-authored and ad hoc workflow already goes through (LoadBytes,
// which runs Parse -> ApplyDefaults -> Validate) BEFORE writing anything
// durable — mirroring SynthesizeAdHoc's own established discipline: a
// bad workflow must fail loudly at save time, never produce a file a
// later `kairos run` discovers is broken (AGENTS.md rule 1). Refuses to
// overwrite an existing flow of the same name — this pass has no update
// semantics; overwriting a running/forkable definition's file out from
// under it is a real correctness hazard (see L18-fork-replay-verify.md's
// DefinitionRef-must-stay-readable invariant), not just an inconvenience.
func SaveFlow(homeDir, name string, yamlText []byte) (path string, err error) {
	if !validFlowName(name) {
		return "", fmt.Errorf("registry: flow name %q must be a bare filename component (no path separators, no leading dot)", name)
	}

	dir := filepath.Join(homeDir, "flows")
	outPath := filepath.Join(dir, name+".yaml")

	if _, err := LoadBytes(yamlText, outPath); err != nil {
		return "", fmt.Errorf("registry: workflow is invalid: %w", err)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("registry: creating flows dir: %w", err)
	}
	if _, statErr := os.Stat(outPath); statErr == nil {
		return "", fmt.Errorf("registry: a flow named %q already exists at %s", name, outPath)
	}
	if err := os.WriteFile(outPath, yamlText, 0o600); err != nil {
		return "", fmt.Errorf("registry: writing flow: %w", err)
	}
	return outPath, nil
}

// ListFlowDefinitions lists every saved flow under $KAIROS_HOME/flows, in
// filename order. An empty/missing directory returns an empty slice, not
// an error — no flow has ever been saved is not a failure.
func ListFlowDefinitions(homeDir string) ([]FlowDefinitionInfo, error) {
	dir := filepath.Join(homeDir, "flows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("registry: reading flows dir: %w", err)
	}
	out := make([]FlowDefinitionInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".yaml")
		out = append(out, FlowDefinitionInfo{Name: name, Path: filepath.Join(dir, e.Name())})
	}
	return out, nil
}

// GetFlowDefinition resolves a saved flow's real path by name, for
// `kairos flow run <name>` / the web UI's "run this flow" affordance —
// both then dispatch through the identical CreateRun path any hand-run
// `kairos run <file>` already uses; this is name resolution only, never
// a special-cased run mechanism.
func GetFlowDefinition(homeDir, name string) (FlowDefinitionInfo, bool, error) {
	if !validFlowName(name) {
		return FlowDefinitionInfo{}, false, fmt.Errorf("registry: invalid flow name %q", name)
	}
	path := filepath.Join(homeDir, "flows", name+".yaml")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return FlowDefinitionInfo{}, false, nil
		}
		return FlowDefinitionInfo{}, false, err
	}
	return FlowDefinitionInfo{Name: name, Path: path}, true, nil
}
