package registry_test

import (
	"testing"

	"github.com/williamokano/kairos/internal/registry"
)

func TestLoad_restartPolicyDefaultsToFailToHumanWhenSideEffectFreeIsUnset(t *testing.T) {
	def, err := registry.LoadBytes([]byte(`
name: t
nodes:
  - id: n1
    actor: shell
    output: { x: "string!" }
`), "t.yaml")
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	if def.Nodes[0].RestartPolicy != registry.RestartFailToHuman {
		t.Errorf("RestartPolicy = %q, want %q", def.Nodes[0].RestartPolicy, registry.RestartFailToHuman)
	}
}

func TestLoad_restartPolicyDefaultsToRerunWhenSideEffectFreeIsTrue(t *testing.T) {
	def, err := registry.LoadBytes([]byte(`
name: t
nodes:
  - id: n1
    actor: shell
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

func TestLoad_acceptsRestartPolicyAdopt(t *testing.T) {
	def, err := registry.LoadBytes([]byte(`
name: t
nodes:
  - id: n1
    actor: shell
    restartPolicy: adopt
    output: { x: "string!" }
`), "t.yaml")
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	if def.Nodes[0].RestartPolicy != registry.RestartAdopt {
		t.Errorf("RestartPolicy = %q, want %q", def.Nodes[0].RestartPolicy, registry.RestartAdopt)
	}
}
