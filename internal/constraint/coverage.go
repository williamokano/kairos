package constraint

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/executor/local"
)

// evaluateCoverage implements 05-gates.md's "coverage" kind: run Command,
// then CoverageThen, capture a numeric percentage from the second
// command's stdout via CoverageCaptureRegex, and compare it as a typed
// float against CoverageMin — "threshold gates must compare numbers,"
// never a regex over the acceptable range. Baseline-vs-base-ref
// comparison (05-gates.md's `baseline: git`) is Future work, not built
// here — see L11-policy-secrets.md's Documented decisions.
func (e *Evaluator) evaluateCoverage(ctx context.Context, in Input) (Result, error) {
	start := time.Now()
	dir := in.Dir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{}, fmt.Errorf("creating gate scratch dir: %w", err)
	}

	if _, err := e.runCoverageStep(ctx, in, filepath.Join(dir, "run"), in.Gate.Command); err != nil {
		return e.gitFailure(in, start, err.Error()), nil
	}
	out, err := e.runCoverageStep(ctx, in, filepath.Join(dir, "then"), in.Gate.CoverageThen)
	if err != nil {
		return e.gitFailure(in, start, err.Error()), nil
	}

	re, err := regexp.Compile(in.Gate.CoverageCaptureRegex)
	if err != nil {
		return e.gitFailure(in, start, fmt.Sprintf("compiling capture regex: %v", err)), nil
	}
	m := re.FindStringSubmatch(out)
	if len(m) < 2 {
		return e.gitFailure(in, start, fmt.Sprintf("capture regex %q did not match coverage output", in.Gate.CoverageCaptureRegex)), nil
	}
	value, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return e.gitFailure(in, start, fmt.Sprintf("captured value %q is not a number: %v", m[1], err)), nil
	}

	dur := msToDuration(start)
	if value >= in.Gate.CoverageMin {
		return Result{Passed: true, Reason: fmt.Sprintf("coverage %.1f%% >= %.1f%%", value, in.Gate.CoverageMin), DurationMs: dur}, nil
	}
	msg := in.Gate.Message
	if msg == "" {
		msg = fmt.Sprintf("coverage %.1f%% is below %.1f%%", value, in.Gate.CoverageMin)
	}
	return Result{
		Passed: false, Reason: msg, DurationMs: dur,
		Findings: []domain.Finding{{ID: in.Gate.ID, Message: msg, Severity: severityOrDefault(in.Gate)}},
	}, nil
}

func (e *Evaluator) runCoverageStep(ctx context.Context, in Input, dir string, argv []string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("empty command")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	binPath, err := local.LookPath(argv[0])
	if err != nil {
		return "", fmt.Errorf("resolving %q: %w", argv[0], err)
	}
	started, err := e.exec.Start(ctx, local.ExecSpec{
		Dir:     dir,
		WorkDir: in.WorkDir,
		Env:     []string{"PATH=/usr/bin:/bin:/usr/local/bin", "HOME=" + dir},
		Argv:    append([]string{binPath}, argv[1:]...),
	})
	if err != nil {
		return "", fmt.Errorf("starting %q: %w", argv[0], err)
	}
	res, err := e.exec.Wait(ctx, started.PID)
	if err != nil {
		return "", fmt.Errorf("waiting for %q: %w", argv[0], err)
	}
	stdout, _ := os.ReadFile(filepath.Join(dir, "stdout.log"))
	if res.ExitCode != 0 {
		return "", fmt.Errorf("%q exited %d", argv[0], res.ExitCode)
	}
	return string(stdout), nil
}
