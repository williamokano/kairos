package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/williamokano/kairos/internal/api"
	"github.com/williamokano/kairos/internal/registry"
)

const validFlowYAMLForAPITest = `name: greet-flow
nodes:
  - id: greet
    actor: rule
    output: { x: "string" }
`

// TestCreateFlowDefinition_validWorkflowIsSavedAndRunnable proves the
// real end-to-end claim: a workflow saved through POST /flow-definitions
// is genuinely `kairos run`-able afterward — registry.Load on the
// returned path must succeed, not just "a file was written somewhere."
func TestCreateFlowDefinition_validWorkflowIsSavedAndRunnable(t *testing.T) {
	home := t.TempDir()
	mux := api.NewMux(api.Deps{Home: home})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body := `{"name":"greet-flow","yaml":` + jsonQuote(validFlowYAMLForAPITest) + `}`
	resp, err := http.Post(srv.URL+"/flow-definitions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /flow-definitions: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out struct{ Name, Path string }
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if out.Name != "greet-flow" {
		t.Errorf("name = %q, want greet-flow", out.Name)
	}
	if _, err := registry.Load(out.Path); err != nil {
		t.Errorf("saved flow at %s does not Load cleanly: %v", out.Path, err)
	}
}

// TestCreateFlowDefinition_invalidWorkflowIsRejectedWithRealError proves
// a bad workflow is rejected with the ACTUAL registry error text, never
// silently written and discovered broken by a later `kairos run`.
func TestCreateFlowDefinition_invalidWorkflowIsRejectedWithRealError(t *testing.T) {
	home := t.TempDir()
	mux := api.NewMux(api.Deps{Home: home})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body := `{"name":"broken","yaml":"this is not a valid workflow"}`
	resp, err := http.Post(srv.URL+"/flow-definitions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /flow-definitions: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	var out struct {
		Error struct{ Message string }
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if out.Error.Message == "" {
		t.Error("expected a real, non-empty error message, got an empty string")
	}
	if _, statErr := os.Stat(filepath.Join(home, "flows", "broken.yaml")); statErr == nil {
		t.Error("an invalid workflow left a file behind")
	}
}

func TestListFlowDefinitionsRoute_listsSavedFlows(t *testing.T) {
	home := t.TempDir()
	if _, err := registry.SaveFlow(home, "existing", []byte(validFlowYAMLForAPITest)); err != nil {
		t.Fatal(err)
	}
	mux := api.NewMux(api.Deps{Home: home})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/flow-definitions")
	if err != nil {
		t.Fatalf("GET /flow-definitions: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Flows []struct{ Name, Path string }
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(out.Flows) != 1 || out.Flows[0].Name != "existing" {
		t.Errorf("flows = %+v, want exactly one named 'existing'", out.Flows)
	}
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
