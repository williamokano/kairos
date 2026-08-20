package registry_test

import (
	"strings"
	"testing"

	"github.com/williamokano/kairos/internal/registry"
)

const validSpawnYAML = `
name: t
nodes:
  - id: plan
    actor: shell
    output: { tasks: ["string"] }
  - id: fanout
    actor: spawn
    spawn:
      workflow: child
      forEach: "$.outputs.plan.tasks"
      strategy: bounded(3)
      inheritWorkspace: clone
    join: { mode: waitAll, onChildFailure: fail }
`

func TestLoad_wellFormedSpawnNodePublishes(t *testing.T) {
	def, err := registry.LoadBytes([]byte(validSpawnYAML), "t.yaml")
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	fanout := def.Nodes[1]
	if fanout.Workspace != registry.WorkspaceNone {
		t.Errorf("Workspace = %q, want %q (03-workflows.md: a coordinator costs nothing at all)", fanout.Workspace, registry.WorkspaceNone)
	}
	if fanout.Wait == nil || len(fanout.Wait.On) != 1 || fanout.Wait.On[0].Kind != registry.WaitChildRun {
		t.Fatalf("Wait = %+v, want an implied wait.on: [{kind: child-run}]", fanout.Wait)
	}
	if fanout.Wait.OnTimeout != "park" {
		t.Errorf("Wait.OnTimeout = %q, want the implied default %q", fanout.Wait.OnTimeout, "park")
	}
	if fanout.Join.OnChildFailure != "fail" {
		t.Errorf("Join.OnChildFailure = %q, want %q", fanout.Join.OnChildFailure, "fail")
	}
}

func TestLoad_spawnOnChildFailureDefaultsToFail(t *testing.T) {
	def, err := registry.LoadBytes([]byte(`
name: t
nodes:
  - id: plan
    actor: shell
    output: { tasks: ["string"] }
  - id: fanout
    actor: spawn
    spawn:
      workflow: child
      forEach: "$.outputs.plan.tasks"
      strategy: bounded(1)
      inheritWorkspace: clone
    join: { mode: waitAll }
`), "t.yaml")
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	if got := def.Nodes[1].Join.OnChildFailure; got != "fail" {
		t.Errorf("Join.OnChildFailure = %q, want default %q", got, "fail")
	}
}

func TestLoad_actorSpawnRequiresSpawnBlock(t *testing.T) {
	_, err := registry.LoadBytes([]byte(`
name: t
nodes:
  - id: fanout
    actor: spawn
`), "t.yaml")
	if err == nil || !strings.Contains(err.Error(), "requires a spawn: block") {
		t.Fatalf("err = %v, want a spawn-block-required error", err)
	}
}

func TestLoad_spawnBlockOnNonSpawnActorIsRejected(t *testing.T) {
	yaml := strings.Replace(validSpawnYAML, "actor: spawn", "actor: shell\n    output: { x: \"string\" }", 1)
	_, err := registry.LoadBytes([]byte(yaml), "t.yaml")
	if err == nil || !strings.Contains(err.Error(), "only valid on actor \"spawn\"") {
		t.Fatalf("err = %v, want a spawn-only-on-spawn-actor error", err)
	}
}

func TestLoad_spawnStrategyMustBeBoundedN(t *testing.T) {
	yaml := strings.Replace(validSpawnYAML, "bounded(3)", "unbounded", 1)
	_, err := registry.LoadBytes([]byte(yaml), "t.yaml")
	if err == nil || !strings.Contains(err.Error(), "bounded(N)") {
		t.Fatalf("err = %v, want a bounded(N)-required error", err)
	}
}

func TestLoad_joinModeMustBeWaitAll(t *testing.T) {
	yaml := strings.Replace(validSpawnYAML, "mode: waitAll", "mode: waitAny", 1)
	_, err := registry.LoadBytes([]byte(yaml), "t.yaml")
	if err == nil || !strings.Contains(err.Error(), "waitAll") {
		t.Fatalf("err = %v, want a waitAll-required error", err)
	}
}

func TestLoad_onChildFailureRejectsUnknownValue(t *testing.T) {
	yaml := strings.Replace(validSpawnYAML, "onChildFailure: fail", "onChildFailure: retry", 1)
	_, err := registry.LoadBytes([]byte(yaml), "t.yaml")
	if err == nil || !strings.Contains(err.Error(), "onChildFailure") {
		t.Fatalf("err = %v, want an onChildFailure error", err)
	}
}

func TestLoad_spawnForEachMustReferenceOutputs(t *testing.T) {
	yaml := strings.Replace(validSpawnYAML, `forEach: "$.outputs.plan.tasks"`, `forEach: "plan.tasks"`, 1)
	_, err := registry.LoadBytes([]byte(yaml), "t.yaml")
	if err == nil || !strings.Contains(err.Error(), "forEach") {
		t.Fatalf("err = %v, want a forEach-shape error", err)
	}
}

func TestLoad_spawnInheritWorkspaceMustBeClone(t *testing.T) {
	yaml := strings.Replace(validSpawnYAML, "inheritWorkspace: clone", "inheritWorkspace: snapshot", 1)
	_, err := registry.LoadBytes([]byte(yaml), "t.yaml")
	if err == nil || !strings.Contains(err.Error(), "inheritWorkspace") {
		t.Fatalf("err = %v, want an inheritWorkspace error", err)
	}
}
