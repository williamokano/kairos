package registry

import "testing"

func TestCompileOutputShorthand_requiredScalarField(t *testing.T) {
	schema, err := compileOutputShorthand(map[string]any{"objective": "string!"})
	if err != nil {
		t.Fatalf("compileOutputShorthand: %v", err)
	}
	if err := schema.Validate(map[string]any{"objective": "fix the bug"}); err != nil {
		t.Errorf("expected valid instance to pass: %v", err)
	}
	if err := schema.Validate(map[string]any{}); err == nil {
		t.Error("expected missing required field to fail validation")
	}
}

func TestCompileOutputShorthand_optionalScalarField(t *testing.T) {
	schema, err := compileOutputShorthand(map[string]any{"reason": "string"})
	if err != nil {
		t.Fatalf("compileOutputShorthand: %v", err)
	}
	if err := schema.Validate(map[string]any{}); err != nil {
		t.Errorf("expected an omitted optional field to pass: %v", err)
	}
}

func TestCompileOutputShorthand_arrayOfRequiredStrings(t *testing.T) {
	schema, err := compileOutputShorthand(map[string]any{"tasks": []any{"string!"}})
	if err != nil {
		t.Fatalf("compileOutputShorthand: %v", err)
	}
	if err := schema.Validate(map[string]any{"tasks": []any{"a", "b"}}); err != nil {
		t.Errorf("expected valid array instance to pass: %v", err)
	}
	if err := schema.Validate(map[string]any{}); err == nil {
		t.Error("expected a missing array field (always required) to fail validation")
	}
}

func TestCompileOutputShorthand_nestedObjectIsAlwaysRequired(t *testing.T) {
	schema, err := compileOutputShorthand(map[string]any{
		"meta": map[string]any{"id": "string!"},
	})
	if err != nil {
		t.Fatalf("compileOutputShorthand: %v", err)
	}
	if err := schema.Validate(map[string]any{"meta": map[string]any{"id": "x"}}); err != nil {
		t.Errorf("expected valid nested instance to pass: %v", err)
	}
	if err := schema.Validate(map[string]any{}); err == nil {
		t.Error("expected a missing nested object to fail validation")
	}
}

func TestCompileOutputShorthand_unknownTypeIsAnError(t *testing.T) {
	if _, err := compileOutputShorthand(map[string]any{"x": "widget!"}); err == nil {
		t.Error("expected an unknown shorthand type to be an error")
	}
}
