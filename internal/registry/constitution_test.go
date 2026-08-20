package registry_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/williamokano/kairos/internal/registry"
)

func TestMergeConstitution_baselineAlwaysPresent(t *testing.T) {
	merged, _, err := registry.MergeConstitution("", "")
	if err != nil {
		t.Fatalf("MergeConstitution: %v", err)
	}
	for _, id := range []string{"guardrails-untouched", "no-secrets", "clean-tree"} {
		if _, ok := merged[id]; !ok {
			t.Errorf("baseline gate %q missing from merge with no repo/project layers", id)
		}
	}
}

func TestMergeConstitution_repoLayerIsMergedIn(t *testing.T) {
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "repo-constitution.yaml")
	writeYAML(t, repoPath, `
gates:
  extra-repo-gate:
    kind: expr
    check: { expr: "true" }
`)
	merged, _, err := registry.MergeConstitution(repoPath, "")
	if err != nil {
		t.Fatalf("MergeConstitution: %v", err)
	}
	if _, ok := merged["extra-repo-gate"]; !ok {
		t.Fatal("repo-layer gate was not merged in")
	}
	if _, ok := merged["guardrails-untouched"]; !ok {
		t.Fatal("baseline gate should still be present alongside a repo layer")
	}
}

func TestMergeConstitution_projectLayerIsAuthoritativeOverRepo(t *testing.T) {
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "repo-constitution.yaml")
	projectPath := filepath.Join(dir, "project-constitution.yaml")
	writeYAML(t, repoPath, `
gates:
  shared-id:
    kind: expr
    check: { expr: "true" }
    message: "from repo"
`)
	writeYAML(t, projectPath, `
gates:
  shared-id:
    kind: expr
    check: { expr: "true" }
    message: "from project"
`)
	merged, _, err := registry.MergeConstitution(repoPath, projectPath)
	if err != nil {
		t.Fatalf("MergeConstitution: %v", err)
	}
	if got := merged["shared-id"].Message; got != "from project" {
		t.Errorf("Message = %q, want %q — project layer must win over repo layer", got, "from project")
	}
}

func TestMergeConstitution_workflowInlineWinsOverEveryLayer(t *testing.T) {
	dir := t.TempDir()
	projectPath := filepath.Join(dir, "project-constitution.yaml")
	writeYAML(t, projectPath, `
gates:
  shared-id:
    kind: expr
    check: { expr: "true" }
    message: "from project"
`)
	wfPath := filepath.Join(dir, "workflow.yaml")
	writeYAML(t, wfPath, `
name: test
gates:
  shared-id:
    kind: expr
    check: { expr: "true" }
    message: "from workflow"
nodes:
  - id: n1
    actor: rule
`)
	def, err := registry.LoadWithConstitution(wfPath, "", projectPath)
	if err != nil {
		t.Fatalf("LoadWithConstitution: %v", err)
	}
	if got := def.Gates["shared-id"].Message; got != "from workflow" {
		t.Errorf("Message = %q, want %q — a workflow's own inline gates: is the most specific layer", got, "from workflow")
	}
}

func TestLoadConstitutionGates_hashPinningDetectsAModifiedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "constitution.yaml")
	writeYAML(t, path, `
gates:
  g1: { kind: expr, check: { expr: "true" } }
`)
	_, bytes1, err := registry.LoadConstitutionGates(path)
	if err != nil {
		t.Fatalf("LoadConstitutionGates: %v", err)
	}

	writeYAML(t, path, `
gates:
  g1: { kind: expr, check: { expr: "false" } }
`)
	_, bytes2, err := registry.LoadConstitutionGates(path)
	if err != nil {
		t.Fatalf("LoadConstitutionGates: %v", err)
	}

	if string(bytes1) == string(bytes2) {
		t.Fatal("expected the raw bytes to differ after modifying the file — hash-pinning depends on detecting exactly this")
	}
}

func TestLoadConstitutionGates_missingFileIsNotAnError(t *testing.T) {
	gates, bytes, err := registry.LoadConstitutionGates(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("LoadConstitutionGates: %v", err)
	}
	if len(gates) != 0 {
		t.Errorf("gates = %v, want empty", gates)
	}
	if bytes != nil {
		t.Errorf("bytes = %v, want nil", bytes)
	}
}

func writeYAML(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}
