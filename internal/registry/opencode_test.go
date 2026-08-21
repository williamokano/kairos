package registry_test

import (
	"testing"

	"github.com/williamokano/kairos/internal/registry"
)

// TestLoad_opencodeMatchesClaudeCodexGeminiDefaults proves NL-29's
// registry wiring for the new "opencode" actor kind
// (internal/registry/defaults.go's agentActors,
// internal/engine/admission.go's llmActorKinds, and dispatch.go's actor
// switch all needed the same one-entry addition): a workspace: write
// opencode node gets the write-node retry upgrade
// (defaultRetry/agentActors) exactly like a claude node does, since both
// are "agent actors" for retry-defaulting purposes.
func TestLoad_opencodeMatchesClaudeCodexGeminiDefaults(t *testing.T) {
	def, err := registry.LoadBytes([]byte(`
name: t
nodes:
  - id: n1
    actor: opencode
    workspace: write
    output: { ok: "bool!" }
`), "t.yaml")
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	nd := def.Nodes[0]

	if nd.Retry.MaxAttempts != 2 {
		t.Errorf("Retry.MaxAttempts = %d, want 2 (agentActors write-node upgrade)", nd.Retry.MaxAttempts)
	}
	if !nd.Retry.FreshWorkspace {
		t.Error("Retry.FreshWorkspace = false, want true (agentActors write-node upgrade)")
	}

	// RestartPolicy default is independent of actor kind (it keys off
	// SideEffectFree, not agentActors) — asserted here anyway to pin the
	// whole node's defaulted shape down in one test, matching this
	// package's existing restartpolicy_test.go pattern.
	if nd.RestartPolicy != registry.RestartFailToHuman {
		t.Errorf("RestartPolicy = %q, want %q", nd.RestartPolicy, registry.RestartFailToHuman)
	}
}

// TestLoad_opencodeRequiresOutputSchema proves opencode is a real "agent
// actor" for L8's typed-contract requirement too, not just retry
// defaulting: a node with no output/outputSchema is rejected exactly
// like a claude node would be (parse_test.go's
// TestLoad_requiresOutputSchemaForAgentActors is the claude version of
// this same assertion).
func TestLoad_opencodeRequiresOutputSchema(t *testing.T) {
	_, err := registry.Load("testdata/invalid/missing-output-schema-opencode.yaml")
	if err == nil {
		t.Fatal("expected missing output/outputSchema on an opencode-actor node to be rejected")
	}
}

// TestLoad_opencodeSideEffectFreeStillDefaultsToRerun proves opencode
// doesn't accidentally acquire different RestartPolicy behaviour from
// claude/shell: sideEffectFree: true still wins RestartRerun regardless
// of actor kind (restartpolicy_test.go's existing shell-actor coverage,
// repeated here for opencode specifically since that field's defaulting
// doesn't consult agentActors at all and a regression there would be
// actor-kind-independent).
func TestLoad_opencodeSideEffectFreeStillDefaultsToRerun(t *testing.T) {
	def, err := registry.LoadBytes([]byte(`
name: t
nodes:
  - id: n1
    actor: opencode
    sideEffectFree: true
    output: { x: "string!" }
`), "t.yaml")
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	if def.Nodes[0].RestartPolicy != registry.RestartRerun {
		t.Errorf("RestartPolicy = %q, want %q", def.Nodes[0].RestartPolicy, registry.RestartRerun)
	}
}
