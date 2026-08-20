package constraint

import (
	"testing"

	"github.com/williamokano/kairos/internal/registry"
)

// White-box test (package constraint, not constraint_test) for
// parseGolangciJSON — unexported, cheapest to exercise directly.
func TestParseGolangciJSON_decodesIssuesIntoFindings(t *testing.T) {
	body := []byte(`{
		"Issues": [
			{"FromLinter": "govet", "Text": "unreachable code", "Severity": "", "Pos": {"Filename": "main.go", "Line": 42, "Column": 3}},
			{"FromLinter": "staticcheck", "Text": "unused variable x", "Severity": "warning", "Pos": {"Filename": "util.go", "Line": 7, "Column": 1}}
		]
	}`)
	findings, err := parseGolangciJSON(body)
	if err != nil {
		t.Fatalf("parseGolangciJSON: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("len(findings) = %d, want 2", len(findings))
	}
	if findings[0].Severity != "high" {
		t.Errorf("findings[0].Severity = %q, want the default %q (input left it empty)", findings[0].Severity, "high")
	}
	if findings[1].Severity != "warning" {
		t.Errorf("findings[1].Severity = %q, want %q (input set it explicitly)", findings[1].Severity, "warning")
	}
	if findings[0].Message == "" || findings[1].Message == "" {
		t.Error("expected non-empty Message on every finding")
	}
}

func TestParseGolangciJSON_emptyReportIsNotAnError(t *testing.T) {
	findings, err := parseGolangciJSON([]byte(`{"Issues": []}`))
	if err != nil {
		t.Fatalf("parseGolangciJSON: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("len(findings) = %d, want 0", len(findings))
	}
}

func TestParseGolangciJSON_malformedInputReturnsErrorNotPanic(t *testing.T) {
	if _, err := parseGolangciJSON([]byte(`not json at all {{{`)); err == nil {
		t.Fatal("expected an error for malformed input")
	}
	if _, err := parseGolangciJSON(nil); err == nil {
		t.Fatal("expected an error for empty input")
	}
}

func TestFindingsFrom_unrecognisedFormatReturnsNilNotPanic(t *testing.T) {
	got := findingsFrom(registry.GateDef{FindingsFormat: "something-unheard-of"}, []byte("whatever"))
	if got != nil {
		t.Errorf("findingsFrom = %v, want nil for an unrecognised format", got)
	}
}
