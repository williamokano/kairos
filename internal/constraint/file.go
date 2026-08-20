package constraint

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/williamokano/kairos/internal/domain"
)

// evaluateFile implements 05-gates.md's "file" kind: exists/absent glob
// checks rooted at the workspace, real os.Stat/filepath.Glob, no
// subprocess. In-process and free, matching the doc's placement/cost
// table.
func (e *Evaluator) evaluateFile(in Input) Result {
	start := time.Now()
	var findings []domain.Finding

	for _, pattern := range in.Gate.FileExists {
		matches, err := filepath.Glob(filepath.Join(in.WorkDir, pattern))
		if err != nil {
			findings = append(findings, domain.Finding{ID: in.Gate.ID, Message: fmt.Sprintf("invalid exists pattern %q: %v", pattern, err), Severity: severityOrDefault(in.Gate)})
			continue
		}
		if len(matches) == 0 {
			findings = append(findings, domain.Finding{ID: in.Gate.ID, Message: fmt.Sprintf("expected a file matching %q, found none", pattern), Severity: severityOrDefault(in.Gate)})
		}
	}
	for _, pattern := range in.Gate.FileAbsent {
		matches, err := filepath.Glob(filepath.Join(in.WorkDir, pattern))
		if err != nil {
			findings = append(findings, domain.Finding{ID: in.Gate.ID, Message: fmt.Sprintf("invalid absent pattern %q: %v", pattern, err), Severity: severityOrDefault(in.Gate)})
			continue
		}
		for _, m := range matches {
			if _, statErr := os.Stat(m); statErr == nil {
				findings = append(findings, domain.Finding{ID: in.Gate.ID, Message: fmt.Sprintf("file %q matches forbidden pattern %q", m, pattern), Severity: severityOrDefault(in.Gate)})
			}
		}
	}

	dur := msToDuration(start)
	if len(findings) == 0 {
		return Result{Passed: true, Reason: "all file assertions satisfied", DurationMs: dur}
	}
	msg := in.Gate.Message
	if msg == "" {
		msg = findings[0].Message
	}
	return Result{Passed: false, Reason: msg, DurationMs: dur, Findings: findings}
}
