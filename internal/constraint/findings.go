package constraint

import (
	"encoding/json"
	"fmt"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/registry"
)

// findingsFrom turns a command gate's raw stdout into structured findings
// per its declared FindingsFormat — 05-gates.md's "findings-were-addressed"
// loop: a failing gate's output becomes the next attempt's input via
// domain's existing LoopGuard-bounded retry (advanceNodeGatesEvaluated),
// not a parallel retry mechanism. An unrecognised or empty format, or a
// parse failure, returns nil — the caller falls back to one synthetic
// finding built from the exit code and raw evidence, never a panic
// (AGENTS §4 rule 1).
func findingsFrom(gd registry.GateDef, stdout []byte) []domain.Finding {
	switch gd.FindingsFormat {
	case "golangci-json":
		findings, err := parseGolangciJSON(stdout)
		if err != nil {
			return nil
		}
		return findings
	default:
		return nil
	}
}

// golangciReport is the subset of `golangci-lint run --out-format=json`'s
// schema this adapter reads. The format is documented and stable; fields
// not needed here are simply not decoded.
type golangciReport struct {
	Issues []golangciIssue `json:"Issues"`
}

type golangciIssue struct {
	FromLinter string `json:"FromLinter"`
	Text       string `json:"Text"`
	Severity   string `json:"Severity"`
	Pos        struct {
		Filename string `json:"Filename"`
		Line     int    `json:"Line"`
		Column   int    `json:"Column"`
	} `json:"Pos"`
}

// parseGolangciJSON decodes body as a golangci-lint JSON report and turns
// each issue into a domain.Finding. Malformed or empty input returns an
// error rather than panicking; an empty-but-valid report (zero issues)
// returns an empty, non-nil slice and no error.
func parseGolangciJSON(body []byte) ([]domain.Finding, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("constraint: empty golangci-json input")
	}
	var report golangciReport
	if err := json.Unmarshal(body, &report); err != nil {
		return nil, fmt.Errorf("constraint: decoding golangci-json: %w", err)
	}
	findings := make([]domain.Finding, 0, len(report.Issues))
	for _, iss := range report.Issues {
		severity := iss.Severity
		if severity == "" {
			severity = "high"
		}
		findings = append(findings, domain.Finding{
			ID:       fmt.Sprintf("%s:%s:%d", iss.FromLinter, iss.Pos.Filename, iss.Pos.Line),
			Message:  fmt.Sprintf("%s:%d: %s (%s)", iss.Pos.Filename, iss.Pos.Line, iss.Text, iss.FromLinter),
			Severity: severity,
		})
	}
	return findings, nil
}
