package registry_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/williamokano/kairos/internal/registry"
)

const validFlowYAML = `name: greet-flow
nodes:
  - id: greet
    actor: rule
    output: { x: "string" }
`

func TestSaveFlow_validWorkflowIsSavedAndRunnable(t *testing.T) {
	home := t.TempDir()

	path, err := registry.SaveFlow(home, "greet-flow", []byte(validFlowYAML))
	if err != nil {
		t.Fatalf("SaveFlow: %v", err)
	}
	wantPath := filepath.Join(home, "flows", "greet-flow.yaml")
	if path != wantPath {
		t.Errorf("path = %s, want %s", path, wantPath)
	}

	// The whole point of validating via LoadBytes before writing is that
	// what's on disk is genuinely `kairos run`-able afterward — prove it
	// by loading it back through the exact same path a real dispatch
	// would use.
	if _, err := registry.Load(path); err != nil {
		t.Errorf("saved flow does not Load cleanly: %v", err)
	}
}

func TestSaveFlow_invalidWorkflowIsRejectedAndLeavesNoFile(t *testing.T) {
	home := t.TempDir()

	_, err := registry.SaveFlow(home, "broken-flow", []byte("this is not a valid workflow at all"))
	if err == nil {
		t.Fatal("expected SaveFlow to reject an invalid workflow")
	}
	if _, statErr := os.Stat(filepath.Join(home, "flows", "broken-flow.yaml")); statErr == nil {
		t.Error("SaveFlow left a file behind despite rejecting the workflow")
	}
}

func TestSaveFlow_rejectsPathTraversalInName(t *testing.T) {
	home := t.TempDir()
	if _, err := registry.SaveFlow(home, "../../etc/evil", []byte(validFlowYAML)); err == nil {
		t.Fatal("expected SaveFlow to reject a name containing path separators")
	}
}

func TestSaveFlow_refusesToOverwriteAnExistingFlow(t *testing.T) {
	home := t.TempDir()
	if _, err := registry.SaveFlow(home, "dup", []byte(validFlowYAML)); err != nil {
		t.Fatalf("first SaveFlow: %v", err)
	}
	if _, err := registry.SaveFlow(home, "dup", []byte(validFlowYAML)); err == nil {
		t.Fatal("expected the second SaveFlow with the same name to fail")
	}
}

func TestListFlowDefinitions_listsSavedFlows(t *testing.T) {
	home := t.TempDir()
	if _, err := registry.SaveFlow(home, "one", []byte(validFlowYAML)); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.SaveFlow(home, "two", []byte(validFlowYAML)); err != nil {
		t.Fatal(err)
	}

	flows, err := registry.ListFlowDefinitions(home)
	if err != nil {
		t.Fatalf("ListFlowDefinitions: %v", err)
	}
	if len(flows) != 2 {
		t.Fatalf("got %d flows, want 2", len(flows))
	}
}

func TestListFlowDefinitions_emptyDirIsNotAnError(t *testing.T) {
	home := t.TempDir()
	flows, err := registry.ListFlowDefinitions(home)
	if err != nil {
		t.Fatalf("ListFlowDefinitions on a home with no flows dir: %v", err)
	}
	if len(flows) != 0 {
		t.Errorf("got %d flows, want 0", len(flows))
	}
}

func TestGetFlowDefinition_foundAndNotFound(t *testing.T) {
	home := t.TempDir()
	if _, err := registry.SaveFlow(home, "exists", []byte(validFlowYAML)); err != nil {
		t.Fatal(err)
	}

	info, ok, err := registry.GetFlowDefinition(home, "exists")
	if err != nil || !ok {
		t.Fatalf("GetFlowDefinition(exists) = %v, %v, %v", info, ok, err)
	}

	_, ok, err = registry.GetFlowDefinition(home, "does-not-exist")
	if err != nil {
		t.Fatalf("GetFlowDefinition(does-not-exist): %v", err)
	}
	if ok {
		t.Error("expected ok=false for a flow that was never saved")
	}
}
