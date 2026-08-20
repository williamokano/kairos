package effect

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/williamokano/kairos/internal/executor/local"
)

// GitPush is the "git.push" builtin: `git push origin HEAD:<branch>` in
// req.WorkDir, spawned through internal/executor/local. Not compensable
// in this document's scope — 05-gates.md declares no "revert" for
// git.push (unlike gh.pr.create's `gh pr close`), and force-reverting a
// remote ref this codebase just pushed to is exactly the kind of
// destructive, silently-run action AGENTS §4 rule 7 forbids automating.
type GitPush struct {
	Exec local.Executor
}

func (GitPush) Kind() string { return "git.push" }

func (p GitPush) Attempt(ctx context.Context, req Request) (Result, error) {
	branch := req.Args["branch"]
	if branch == "" {
		return Result{}, fmt.Errorf("git.push: args[\"branch\"] is required")
	}
	if err := os.MkdirAll(req.Dir, 0o755); err != nil {
		return Result{}, fmt.Errorf("git.push: creating scratch dir: %w", err)
	}
	code, _, err := runGit(ctx, p.Exec, req, []string{"push", "origin", "HEAD:refs/heads/" + branch})
	if err != nil {
		return Result{}, fmt.Errorf("git.push: %w", err)
	}
	if code != 0 {
		return Result{Outcome: Failed, Reason: fmt.Sprintf("git push exited %d", code)}, nil
	}
	return Result{Outcome: Applied, ExternalRef: branch}, nil
}

// Probe re-derives whether branch's remote ref matches the local
// workspace's current HEAD — the only evidence available after a crash
// mid-push, since git itself keeps no attempt log. A mismatch or a
// missing ref means Probe cannot say either way, so ok=false — the
// caller records effect.unknown rather than guessing.
func (p GitPush) Probe(ctx context.Context, req Request) (Result, bool, error) {
	branch := req.Args["branch"]
	if branch == "" {
		return Result{}, false, nil
	}
	localSHA, err := runGitCapture(ctx, p.Exec, req, []string{"rev-parse", "HEAD"})
	if err != nil {
		return Result{}, false, nil
	}
	remoteOut, err := runGitCapture(ctx, p.Exec, req, []string{"ls-remote", "origin", "refs/heads/" + branch})
	if err != nil || remoteOut == "" {
		return Result{}, false, nil
	}
	remoteSHA := strings.Fields(remoteOut)[0]
	if remoteSHA == strings.TrimSpace(localSHA) {
		return Result{Outcome: Applied, ExternalRef: branch}, true, nil
	}
	return Result{}, false, nil
}

func (GitPush) Compensate(context.Context, Request, string) error {
	return ErrNotCompensable
}

// runGit spawns argv as `git <argv...>` in req.WorkDir through the
// executor chokepoint and returns its exit code and stdout. Each git
// subcommand gets its own subdirectory under req.Dir — Local's Start
// opens stdout.log with O_APPEND, so reusing one dir across the several
// git calls one Attempt/Probe makes (rev-parse, ls-remote, push) would
// silently concatenate their outputs into one file.
func runGit(ctx context.Context, exec local.Executor, req Request, argv []string) (int, string, error) {
	dir := filepath.Join(req.Dir, argv[0])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, "", fmt.Errorf("creating scratch dir for git %v: %w", argv, err)
	}
	bin, err := resolveBinary("git", req.PathPrefix)
	if err != nil {
		return 0, "", err
	}
	started, err := exec.Start(ctx, local.ExecSpec{
		RunID: req.RunID, NodeID: req.NodeID, ExecID: req.ExecID,
		Dir: dir, WorkDir: req.WorkDir,
		Env:  pathEnv(dir, req.PathPrefix),
		Argv: append([]string{bin}, argv...),
	})
	if err != nil {
		return 0, "", fmt.Errorf("starting git %v: %w", argv, err)
	}
	res, err := exec.Wait(ctx, started.PID)
	if err != nil {
		return 0, "", fmt.Errorf("waiting for git %v: %w", argv, err)
	}
	out := readStdout(dir)
	return res.ExitCode, out, nil
}

func runGitCapture(ctx context.Context, exec local.Executor, req Request, argv []string) (string, error) {
	code, out, err := runGit(ctx, exec, req, argv)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("git %v exited %d", argv, code)
	}
	return out, nil
}

func readStdout(dir string) string {
	f, err := os.Open(filepath.Join(dir, "stdout.log"))
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	var sb strings.Builder
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		sb.WriteString(scanner.Text())
		sb.WriteString("\n")
	}
	return sb.String()
}
