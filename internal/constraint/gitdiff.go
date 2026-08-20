package constraint

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/executor/local"
)

// runGit spawns a real `git` subprocess through internal/executor/local
// — the one execution chokepoint (AGENTS §2) — never os/exec directly,
// matching every other gate kind in this package. It returns stdout as a
// string; a non-zero exit is an error, since every git invocation this
// file makes is expected to succeed against a well-formed workspace.
func (e *Evaluator) runGit(ctx context.Context, workDir, dir string, args ...string) (string, error) {
	binPath, err := local.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("resolving git: %w", err)
	}
	started, err := e.exec.Start(ctx, local.ExecSpec{
		Dir:     dir,
		WorkDir: workDir,
		Env:     []string{"PATH=/usr/bin:/bin:/usr/local/bin", "HOME=" + dir},
		Argv:    append([]string{binPath}, args...),
	})
	if err != nil {
		return "", fmt.Errorf("starting git %v: %w", args, err)
	}
	res, err := e.exec.Wait(ctx, started.PID)
	if err != nil {
		return "", fmt.Errorf("waiting for git %v: %w", args, err)
	}
	stdout, _ := os.ReadFile(filepath.Join(dir, "stdout.log"))
	if res.ExitCode != 0 {
		return "", fmt.Errorf("git %v exited %d: %s", args, res.ExitCode, string(stdout))
	}
	return string(stdout), nil
}

// evaluateGitDiff implements 05-gates.md's "git-diff" kind: scope and
// shape assertions over a real git diff/status, computed in Go rather
// than shelled out to a pipeline. Covers pathsForbidden, mustTouch,
// maxFiles, maxLines, noBinary (diff-shape checks, requiring BaseRef) and
// dirty/staged (working-tree checks, e.g. the built-in clean-tree gate,
// which need no BaseRef at all).
func (e *Evaluator) evaluateGitDiff(ctx context.Context, in Input) (Result, error) {
	start := time.Now()
	var findings []domain.Finding
	dir := in.Dir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{}, fmt.Errorf("creating gate scratch dir: %w", err)
	}

	gd := in.Gate
	if gd.GitDiffDirty != nil || gd.GitDiffStaged != nil {
		status, err := e.runGit(ctx, in.WorkDir, dir, "status", "--porcelain")
		if err != nil {
			return e.gitFailure(in, start, err.Error()), nil
		}
		dirty := strings.TrimSpace(status) != ""
		if gd.GitDiffDirty != nil && dirty != *gd.GitDiffDirty {
			findings = append(findings, domain.Finding{ID: gd.ID, Message: fmt.Sprintf("working tree dirty=%v, want %v", dirty, *gd.GitDiffDirty), Severity: severityOrDefault(gd)})
		}
		// Staged is a subset of dirty (porcelain marks staged changes
		// too); this document does not distinguish staged-only from
		// unstaged-only within one porcelain pass — both check the same
		// signal. A finer distinction (index vs. worktree columns) is
		// Future work.
		if gd.GitDiffStaged != nil && dirty != *gd.GitDiffStaged {
			findings = append(findings, domain.Finding{ID: gd.ID, Message: fmt.Sprintf("working tree staged=%v, want %v", dirty, *gd.GitDiffStaged), Severity: severityOrDefault(gd)})
		}
	}

	if len(gd.GitDiffPathsForbidden) > 0 || len(gd.GitDiffMustTouch) > 0 || gd.GitDiffMaxFiles > 0 || gd.GitDiffMaxLines > 0 || gd.GitDiffNoBinary {
		if in.BaseRef == "" {
			return e.gitFailure(in, start, "git-diff scope checks require a base ref, none configured"), nil
		}
		nameStatus, err := e.runGit(ctx, in.WorkDir, dir, "diff", "--name-status", in.BaseRef+"..HEAD")
		if err != nil {
			return e.gitFailure(in, start, err.Error()), nil
		}
		files := parseNameStatus(nameStatus)

		for _, forbidden := range gd.GitDiffPathsForbidden {
			for _, f := range files {
				if pathMatches(forbidden, f.path) {
					findings = append(findings, domain.Finding{ID: gd.ID, Message: fmt.Sprintf("%s touches forbidden path %q", f.path, forbidden), Severity: severityOrDefault(gd)})
				}
			}
		}
		for _, must := range gd.GitDiffMustTouch {
			touched := false
			for _, f := range files {
				if pathMatches(must, f.path) {
					touched = true
					break
				}
			}
			if !touched {
				findings = append(findings, domain.Finding{ID: gd.ID, Message: fmt.Sprintf("no changed file matches required pattern %q", must), Severity: severityOrDefault(gd)})
			}
		}
		if gd.GitDiffMaxFiles > 0 && len(files) > gd.GitDiffMaxFiles {
			findings = append(findings, domain.Finding{ID: gd.ID, Message: fmt.Sprintf("%d files changed, max is %d", len(files), gd.GitDiffMaxFiles), Severity: severityOrDefault(gd)})
		}
		if gd.GitDiffMaxLines > 0 {
			stat, err := e.runGit(ctx, in.WorkDir, dir, "diff", "--shortstat", in.BaseRef+"..HEAD")
			if err == nil {
				if lines := parseShortstatLines(stat); lines > gd.GitDiffMaxLines {
					findings = append(findings, domain.Finding{ID: gd.ID, Message: fmt.Sprintf("%d lines changed, max is %d", lines, gd.GitDiffMaxLines), Severity: severityOrDefault(gd)})
				}
			}
		}
	}

	dur := msToDuration(start)
	if len(findings) == 0 {
		return Result{Passed: true, Reason: "all git-diff assertions satisfied", DurationMs: dur}, nil
	}
	msg := gd.Message
	if msg == "" {
		msg = findings[0].Message
	}
	return Result{Passed: false, Reason: msg, DurationMs: dur, Findings: findings}, nil
}

func (e *Evaluator) gitFailure(in Input, start time.Time, reason string) Result {
	return Result{
		Passed: false, Reason: reason, DurationMs: msToDuration(start),
		Findings: []domain.Finding{{ID: in.Gate.ID, Message: reason, Severity: severityOrDefault(in.Gate)}},
	}
}

type changedFile struct{ path, status string }

func parseNameStatus(s string) []changedFile {
	var out []changedFile
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		out = append(out, changedFile{status: fields[0], path: fields[len(fields)-1]})
	}
	return out
}

var shortstatRe = regexp.MustCompile(`(\d+) insertion|(\d+) deletion`)

func parseShortstatLines(s string) int {
	total := 0
	for _, m := range shortstatRe.FindAllStringSubmatch(s, -1) {
		for _, g := range m[1:] {
			if g != "" {
				n, _ := strconv.Atoi(g)
				total += n
			}
		}
	}
	return total
}

// evaluateRegex implements 05-gates.md's "regex" kind for
// over: added-lines only (this document's documented scope — see
// GateDef.RegexOver's comment): `git diff --unified=0 <base>..HEAD`,
// parsed in Go, regexp applied to `+` lines only.
func (e *Evaluator) evaluateRegex(ctx context.Context, in Input) (Result, error) {
	start := time.Now()
	if in.BaseRef == "" {
		return e.gitFailure(in, start, "regex (over: added-lines) requires a base ref, none configured"), nil
	}
	dir := in.Dir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{}, fmt.Errorf("creating gate scratch dir: %w", err)
	}
	diff, err := e.runGit(ctx, in.WorkDir, dir, "diff", "--unified=0", in.BaseRef+"..HEAD")
	if err != nil {
		return e.gitFailure(in, start, err.Error()), nil
	}

	re, err := regexp.Compile(in.Gate.RegexAbsent)
	if err != nil {
		return e.gitFailure(in, start, fmt.Sprintf("compiling regex: %v", err)), nil
	}

	var findings []domain.Finding
	currentFile := ""
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+++ b/") {
			currentFile = strings.TrimPrefix(line, "+++ b/")
			continue
		}
		if !strings.HasPrefix(line, "+") || strings.HasPrefix(line, "+++") {
			continue
		}
		if excluded(currentFile, in.Gate.RegexExclude) {
			continue
		}
		content := line[1:]
		if re.MatchString(content) {
			findings = append(findings, domain.Finding{
				ID: in.Gate.ID, Severity: severityOrDefault(in.Gate),
				Message: fmt.Sprintf("%s: matched forbidden pattern %q: %s", currentFile, in.Gate.RegexAbsent, strings.TrimSpace(content)),
			})
		}
	}

	dur := msToDuration(start)
	if len(findings) == 0 {
		return Result{Passed: true, Reason: "no added line matched the forbidden pattern", DurationMs: dur}, nil
	}
	msg := in.Gate.Message
	if msg == "" {
		msg = findings[0].Message
	}
	return Result{Passed: false, Reason: msg, DurationMs: dur, Findings: findings}, nil
}

// excluded reports whether path matches any of patterns. filepath.Match
// does not support `**`, so a minimal prefix/suffix subset is implemented
// directly: "docs/**" excludes anything under docs/, "**/*_test.go"
// excludes any path ending in _test.go regardless of directory. This
// covers 05-gates.md's own examples without pulling in a third-party
// glob library for one gate kind — a fuller `**`-in-the-middle glob is
// Future work, see L11-policy-secrets.md's Documented decisions.
func excluded(path string, patterns []string) bool {
	for _, p := range patterns {
		if pathMatches(p, path) {
			return true
		}
	}
	return false
}

// pathMatches reports whether path matches the single glob pattern —
// used both by excluded (regex kind's exclude list) and evaluateGitDiff
// (pathsForbidden/mustTouch), which need the identical `**` subset (see
// excluded's doc comment for its scope).
func pathMatches(pattern, path string) bool {
	if matched, _ := filepath.Match(pattern, path); matched {
		return true
	}
	if suffix, ok := strings.CutPrefix(pattern, "**/"); ok {
		if matched, _ := filepath.Match(suffix, filepath.Base(path)); matched {
			return true
		}
	}
	if prefix, ok := strings.CutSuffix(pattern, "/**"); ok {
		if strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}
