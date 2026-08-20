package registry

import (
	"os"
	"path/filepath"
	"testing"
)

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

func TestCompileSchemaDocWithRaw_rawBytesDecodeToTheSameDoc(t *testing.T) {
	schema, raw, err := compileSchemaDocWithRaw(map[string]any{"type": "object", "required": []string{"ok"}})
	if err != nil {
		t.Fatalf("compileSchemaDocWithRaw: %v", err)
	}
	if err := schema.Validate(map[string]any{"ok": true}); err != nil {
		t.Errorf("expected valid instance to pass: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("expected non-empty raw bytes")
	}
	if got := string(raw); got == "" || got == "null" {
		t.Errorf("raw = %q, want a marshalled schema document", got)
	}
}

// ValidateFile is L08's file-based validator: kairos check-output and the
// llm actor's repair-turn logic both call it directly rather than each
// re-deriving json-pointer violation lines from a live *jsonschema.Schema.
func TestValidateFile_validInstancePassesWithNoViolations(t *testing.T) {
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "schema.json")
	outputPath := filepath.Join(dir, "output.json")
	writeFile(t, schemaPath, `{"type":"object","required":["ok"],"properties":{"ok":{"type":"boolean"}}}`)
	writeFile(t, outputPath, `{"ok":true}`)

	valid, violations, err := ValidateFile(outputPath, schemaPath)
	if err != nil {
		t.Fatalf("ValidateFile: %v", err)
	}
	if !valid {
		t.Errorf("valid = false, violations = %v, want true", violations)
	}
}

func TestValidateFile_invalidInstanceReportsPointerLines(t *testing.T) {
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "schema.json")
	outputPath := filepath.Join(dir, "output.json")
	writeFile(t, schemaPath, `{"type":"object","required":["ok"],"properties":{"ok":{"type":"boolean"}}}`)
	writeFile(t, outputPath, `{"ok":"not-a-bool"}`)

	valid, violations, err := ValidateFile(outputPath, schemaPath)
	if err != nil {
		t.Fatalf("ValidateFile: %v", err)
	}
	if valid {
		t.Fatal("expected invalid")
	}
	if len(violations) == 0 {
		t.Fatal("expected at least one violation line")
	}
}

func TestValidateFile_missingOutputFileIsAnError(t *testing.T) {
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "schema.json")
	writeFile(t, schemaPath, `{"type":"object"}`)

	if _, _, err := ValidateFile(filepath.Join(dir, "does-not-exist.json"), schemaPath); err == nil {
		t.Error("expected an error for a missing output file")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}
