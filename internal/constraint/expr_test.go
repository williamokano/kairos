package constraint_test

import (
	"context"
	"testing"

	"github.com/williamokano/kairos/internal/constraint"
	"github.com/williamokano/kairos/internal/registry"
)

func TestEvaluate_exprPassesAgainstRealTypedOutput(t *testing.T) {
	e := constraint.New(nil, nil)
	result, err := e.Evaluate(context.Background(), constraint.Input{
		Gate:   registry.GateDef{ID: "has-summary", Kind: registry.GateExpr, Expr: "output.summary != \"\""},
		Output: map[string]any{"summary": "did the thing"},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !result.Passed {
		t.Fatalf("Passed = false, want true; Reason = %q", result.Reason)
	}
	if len(result.Findings) != 0 {
		t.Errorf("Findings = %v, want none on a pass", result.Findings)
	}
}

func TestEvaluate_exprFailsWithAFinding(t *testing.T) {
	e := constraint.New(nil, nil)
	result, err := e.Evaluate(context.Background(), constraint.Input{
		Gate:   registry.GateDef{ID: "has-summary", Kind: registry.GateExpr, Expr: "output.summary != \"\"", Severity: "critical"},
		Output: map[string]any{"summary": ""},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if result.Passed {
		t.Fatal("Passed = true, want false")
	}
	if len(result.Findings) != 1 || result.Findings[0].Severity != "critical" {
		t.Fatalf("Findings = %+v, want one critical-severity finding", result.Findings)
	}
}

// TestEvaluate_exprReferencingMissingFieldFailsSafely proves 05-gates.md's
// requirement directly: an expression over a field the output doesn't
// have must fail with a clear reason, never panic.
func TestEvaluate_exprReferencingMissingFieldFailsSafely(t *testing.T) {
	e := constraint.New(nil, nil)
	result, err := e.Evaluate(context.Background(), constraint.Input{
		Gate:   registry.GateDef{ID: "no-such-field", Kind: registry.GateExpr, Expr: "output.requirements[0].id == \"x\""},
		Output: map[string]any{"summary": "no requirements key here"},
	})
	if err != nil {
		t.Fatalf("Evaluate returned an error instead of a failed Result: %v", err)
	}
	if result.Passed {
		t.Fatal("Passed = true, want false")
	}
	if result.Reason == "" {
		t.Error("expected a non-empty Reason explaining the failure")
	}
	if len(result.Findings) != 1 {
		t.Fatalf("Findings = %v, want exactly one", result.Findings)
	}
}

func TestEvaluate_exprInvalidSyntaxFailsSafely(t *testing.T) {
	e := constraint.New(nil, nil)
	result, err := e.Evaluate(context.Background(), constraint.Input{
		Gate:   registry.GateDef{ID: "bad-syntax", Kind: registry.GateExpr, Expr: "this is not }} valid expr syntax((("},
		Output: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Evaluate returned an error instead of a failed Result: %v", err)
	}
	if result.Passed {
		t.Fatal("Passed = true, want false")
	}
}
