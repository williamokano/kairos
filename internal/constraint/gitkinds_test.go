package constraint_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/williamokano/kairos/internal/admission"
	"github.com/williamokano/kairos/internal/constraint"
	"github.com/williamokano/kairos/internal/registry"
)

// newTestRepoWithBranch creates a real git repo with a base commit, then a
// second commit on top, and returns (repoDir, baseRef) — the base ref
// names the first commit, exactly like 05-gates.md's `{{ .base }}`.
func newTestRepoWithBranch(t *testing.T) (dir, baseRef string) {
	t.Helper()
	dir = t.TempDir()
	run(t, dir, "git", "init", "-q")
	run(t, dir, "git", "config", "user.email", "test@example.com")
	run(t, dir, "git", "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatalf("writing README: %v", err)
	}
	run(t, dir, "git", "add", "README.md")
	run(t, dir, "git", "commit", "-q", "-m", "initial")
	out := runOut(t, dir, "git", "rev-parse", "HEAD")
	baseRef = out
	return dir, baseRef
}

func run(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out.String())
	}
}

func runOut(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %v: %v", name, args, err)
	}
	s := out.String()
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

func writeAndCommit(t *testing.T, dir, path, content, msg string) {
	t.Helper()
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	run(t, dir, "git", "add", path)
	run(t, dir, "git", "commit", "-q", "-m", msg)
}

func TestEvaluate_gitDiffCleanTreePasses(t *testing.T) {
	e := newRealEvaluator(admission.New(admission.Config{}))
	dir, _ := newTestRepoWithBranch(t)
	dirty := false
	result, err := e.Evaluate(context.Background(), constraint.Input{
		Gate:    registry.GateDef{ID: "clean-tree", Kind: registry.GateGitDiff, GitDiffDirty: &dirty, GitDiffStaged: &dirty},
		WorkDir: dir,
		Dir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !result.Passed {
		t.Fatalf("Passed = false, want true; Reason = %q", result.Reason)
	}
}

func TestEvaluate_gitDiffCleanTreeFailsWhenDirty(t *testing.T) {
	e := newRealEvaluator(admission.New(admission.Config{}))
	dir, _ := newTestRepoWithBranch(t)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("modified\n"), 0o600); err != nil {
		t.Fatalf("dirtying tree: %v", err)
	}
	dirty := false
	result, err := e.Evaluate(context.Background(), constraint.Input{
		Gate:    registry.GateDef{ID: "clean-tree", Kind: registry.GateGitDiff, GitDiffDirty: &dirty, GitDiffStaged: &dirty},
		WorkDir: dir,
		Dir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if result.Passed {
		t.Fatal("Passed = true, want false — the tree was dirtied")
	}
}

func TestEvaluate_gitDiffPathsForbiddenCatchesAGuardrailEdit(t *testing.T) {
	e := newRealEvaluator(admission.New(admission.Config{}))
	dir, base := newTestRepoWithBranch(t)
	writeAndCommit(t, dir, ".github/workflows/ci.yml", "name: ci\n", "edit ci")

	result, err := e.Evaluate(context.Background(), constraint.Input{
		Gate:    registry.GateDef{ID: "guardrails-untouched", Kind: registry.GateGitDiff, GitDiffPathsForbidden: []string{".github/**"}},
		WorkDir: dir,
		Dir:     t.TempDir(),
		BaseRef: base,
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if result.Passed {
		t.Fatal("Passed = true, want false — a forbidden path was touched")
	}
}

func TestEvaluate_gitDiffMaxFilesCatchesAnOversizedChange(t *testing.T) {
	e := newRealEvaluator(admission.New(admission.Config{}))
	dir, base := newTestRepoWithBranch(t)
	writeAndCommit(t, dir, "a.txt", "a\n", "add a")
	writeAndCommit(t, dir, "b.txt", "b\n", "add b")

	result, err := e.Evaluate(context.Background(), constraint.Input{
		Gate:    registry.GateDef{ID: "scope", Kind: registry.GateGitDiff, GitDiffMaxFiles: 1},
		WorkDir: dir,
		Dir:     t.TempDir(),
		BaseRef: base,
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if result.Passed {
		t.Fatal("Passed = true, want false — two files changed against a maxFiles: 1 gate")
	}
}

func TestEvaluate_gitDiffRequiresBaseRefForScopeChecks(t *testing.T) {
	e := newRealEvaluator(admission.New(admission.Config{}))
	dir, _ := newTestRepoWithBranch(t)
	result, err := e.Evaluate(context.Background(), constraint.Input{
		Gate:    registry.GateDef{ID: "scope", Kind: registry.GateGitDiff, GitDiffMaxFiles: 1},
		WorkDir: dir,
		Dir:     t.TempDir(),
		// BaseRef deliberately empty.
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if result.Passed {
		t.Fatal("Passed = true, want false — a scope check with no base ref must fail loudly, not guess")
	}
}

func TestEvaluate_regexAddedLinesCatchesATODO(t *testing.T) {
	e := newRealEvaluator(admission.New(admission.Config{}))
	dir, base := newTestRepoWithBranch(t)
	writeAndCommit(t, dir, "main.go", "package main\n// TODO: fix this\n", "add todo")

	result, err := e.Evaluate(context.Background(), constraint.Input{
		Gate:    registry.GateDef{ID: "no-todos", Kind: registry.GateRegex, RegexOver: "added-lines", RegexAbsent: `(TODO|FIXME)`},
		WorkDir: dir,
		Dir:     t.TempDir(),
		BaseRef: base,
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if result.Passed {
		t.Fatal("Passed = true, want false — an added TODO should be caught")
	}
	if len(result.Findings) != 1 {
		t.Fatalf("Findings = %v, want exactly one", result.Findings)
	}
}

func TestEvaluate_regexAddedLinesExcludesMatchingFiles(t *testing.T) {
	e := newRealEvaluator(admission.New(admission.Config{}))
	dir, base := newTestRepoWithBranch(t)
	writeAndCommit(t, dir, "main_test.go", "package main\n// TODO: fix this\n", "add todo in test")

	result, err := e.Evaluate(context.Background(), constraint.Input{
		Gate: registry.GateDef{
			ID: "no-todos", Kind: registry.GateRegex, RegexOver: "added-lines", RegexAbsent: `(TODO|FIXME)`,
			RegexExclude: []string{"**/*_test.go"},
		},
		WorkDir: dir,
		Dir:     t.TempDir(),
		BaseRef: base,
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !result.Passed {
		t.Fatalf("Passed = false, want true — the only TODO is in an excluded _test.go file; Reason=%q", result.Reason)
	}
}

func TestEvaluate_regexPassesWithNoMatch(t *testing.T) {
	e := newRealEvaluator(admission.New(admission.Config{}))
	dir, base := newTestRepoWithBranch(t)
	writeAndCommit(t, dir, "main.go", "package main\nfunc main() {}\n", "clean commit")

	result, err := e.Evaluate(context.Background(), constraint.Input{
		Gate:    registry.GateDef{ID: "no-todos", Kind: registry.GateRegex, RegexOver: "added-lines", RegexAbsent: `(TODO|FIXME)`},
		WorkDir: dir,
		Dir:     t.TempDir(),
		BaseRef: base,
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !result.Passed {
		t.Fatalf("Passed = false, want true; Reason=%q", result.Reason)
	}
}

func TestEvaluate_fileExistsPasses(t *testing.T) {
	e := newRealEvaluator(admission.New(admission.Config{}))
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "present.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	result, err := e.Evaluate(context.Background(), constraint.Input{
		Gate:    registry.GateDef{ID: "f", Kind: registry.GateFile, FileExists: []string{"present.txt"}},
		WorkDir: dir,
		Dir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !result.Passed {
		t.Fatalf("Passed = false, want true; Reason=%q", result.Reason)
	}
}

func TestEvaluate_fileAbsentFailsWhenPresent(t *testing.T) {
	e := newRealEvaluator(admission.New(admission.Config{}))
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forbidden.pyc"), []byte("x"), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	result, err := e.Evaluate(context.Background(), constraint.Input{
		Gate:    registry.GateDef{ID: "f", Kind: registry.GateFile, FileAbsent: []string{"*.pyc"}},
		WorkDir: dir,
		Dir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if result.Passed {
		t.Fatal("Passed = true, want false — a forbidden .pyc file is present")
	}
}

func TestEvaluate_coveragePassesAboveThreshold(t *testing.T) {
	e := newRealEvaluator(admission.New(admission.Config{}))
	result, err := e.Evaluate(context.Background(), constraint.Input{
		Gate: registry.GateDef{
			ID: "cov", Kind: registry.GateCoverage,
			Command:              []string{"true"},
			CoverageThen:         []string{"echo", "total: (statements) 85.3%"},
			CoverageCaptureRegex: `([0-9.]+)%`,
			CoverageMin:          80,
		},
		WorkDir: t.TempDir(),
		Dir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !result.Passed {
		t.Fatalf("Passed = false, want true; Reason=%q", result.Reason)
	}
}

func TestEvaluate_coverageFailsBelowThreshold(t *testing.T) {
	e := newRealEvaluator(admission.New(admission.Config{}))
	result, err := e.Evaluate(context.Background(), constraint.Input{
		Gate: registry.GateDef{
			ID: "cov", Kind: registry.GateCoverage,
			Command:              []string{"true"},
			CoverageThen:         []string{"echo", "total: (statements) 42.0%"},
			CoverageCaptureRegex: `([0-9.]+)%`,
			CoverageMin:          80,
		},
		WorkDir: t.TempDir(),
		Dir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if result.Passed {
		t.Fatal("Passed = true, want false — 42% is below the 80% minimum")
	}
}
