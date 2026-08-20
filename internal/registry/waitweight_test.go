package registry_test

import (
	"testing"

	"github.com/williamokano/kairos/internal/registry"
)

func TestLoad_waitWeightDefaultsToReadForAHumanWait(t *testing.T) {
	def, err := registry.LoadBytes([]byte(`
name: t
nodes:
  - id: approve
    actor: human
    output: { decision: "string!", reason: "string" }
    wait:
      "on": [{ kind: human }]
      onTimeout: park
`), "t.yaml")
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	if def.Nodes[0].Wait.Weight != registry.WeightRead {
		t.Fatalf("Weight = %q, want %q", def.Nodes[0].Wait.Weight, registry.WeightRead)
	}
}

func TestLoad_waitWeightExplicitOverridesDefault(t *testing.T) {
	def, err := registry.LoadBytes([]byte(`
name: t
nodes:
  - id: approve
    actor: human
    output: { decision: "string!", reason: "string" }
    wait:
      "on": [{ kind: human }]
      onTimeout: park
      weight: type
`), "t.yaml")
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	if def.Nodes[0].Wait.Weight != registry.WeightType {
		t.Fatalf("Weight = %q, want %q", def.Nodes[0].Wait.Weight, registry.WeightType)
	}
}

func TestLoad_rejectsAnUnknownWaitWeight(t *testing.T) {
	_, err := registry.LoadBytes([]byte(`
name: t
nodes:
  - id: approve
    actor: human
    output: { decision: "string!", reason: "string" }
    wait:
      "on": [{ kind: human }]
      onTimeout: park
      weight: shrug
`), "t.yaml")
	if err == nil {
		t.Fatal("expected an unknown wait.weight to be rejected at publish time")
	}
}
