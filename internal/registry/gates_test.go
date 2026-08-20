package registry_test

import (
	"testing"

	"github.com/williamokano/kairos/internal/registry"
)

func TestLoad_gatesDefaultToWaivableTrue(t *testing.T) {
	def, err := registry.LoadBytes([]byte(`
name: t
gates:
  lint:
    kind: command
    check: { command: ["golangci-lint", "run"] }
nodes:
  - id: n1
    actor: shell
    output: { x: "string!" }
    gates: [lint]
`), "t.yaml")
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	gd, ok := def.Gates["lint"]
	if !ok {
		t.Fatal("expected gates.lint to be present")
	}
	if !gd.Waivable {
		t.Error("Waivable = false, want true (the default absent an explicit waivable: false)")
	}
}

func TestLoad_gateExplicitWaivableFalseIsPreserved(t *testing.T) {
	def, err := registry.LoadBytes([]byte(`
name: t
gates:
  scope-respected:
    kind: expr
    waivable: false
    check: { expr: "output.filesChanged < 40" }
nodes:
  - id: n1
    actor: shell
    output: { x: "string!" }
    gates: [scope-respected]
`), "t.yaml")
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	if def.Gates["scope-respected"].Waivable {
		t.Error("Waivable = true, want false (explicitly declared)")
	}
}

func TestLoad_gateUnsupportedKindIsRejected(t *testing.T) {
	_, err := registry.LoadBytes([]byte(`
name: t
gates:
  judged-later:
    kind: judged
    check: {}
nodes:
  - id: n1
    actor: shell
    output: { x: "string!" }
`), "t.yaml")
	if err == nil {
		t.Fatal("expected an error for an unsupported gate kind (judged is not implemented by this phase-0 slice)")
	}
}

func TestLoad_commandGateRequiresNonEmptyCommand(t *testing.T) {
	_, err := registry.LoadBytes([]byte(`
name: t
gates:
  empty:
    kind: command
    check: {}
nodes:
  - id: n1
    actor: shell
    output: { x: "string!" }
`), "t.yaml")
	if err == nil {
		t.Fatal("expected an error for a command gate with no check.command")
	}
}

func TestLoad_commandGateAbsoluteWorkdirIsRejectedAtPublish(t *testing.T) {
	_, err := registry.LoadBytes([]byte(`
name: t
gates:
  bad-workdir:
    kind: command
    check: { command: ["true"], workdir: "/etc" }
nodes:
  - id: n1
    actor: shell
    output: { x: "string!" }
    gates: [bad-workdir]
`), "t.yaml")
	if err == nil {
		t.Fatal("expected an error for an absolute gates.*.check.workdir (05-gates.md: absolute paths REJECTED)")
	}
}

// TestLoad_nodeGateReferenceWithNoLocalDefinitionStillPublishes proves the
// deliberate scope-narrowing decision in validate.go: 03-workflows.md's
// own canonical fix-issue.yaml example references gate names
// (build/lint/no-todos/...) no top-level gates: block defines, because
// the real resolution (kairos/baseline + project + repo constitution
// merge) is L11 scope. Publishing must not reject that.
func TestLoad_nodeGateReferenceWithNoLocalDefinitionStillPublishes(t *testing.T) {
	_, err := registry.LoadBytes([]byte(`
name: t
nodes:
  - id: n1
    actor: shell
    output: { x: "string!" }
    gates: [build, lint, guardrails-untouched]
`), "t.yaml")
	if err != nil {
		t.Fatalf("LoadBytes: %v (unresolved gate names must not fail publish — see validate.go)", err)
	}
}
