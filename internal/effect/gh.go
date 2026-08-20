package effect

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/williamokano/kairos/internal/executor/local"
)

// GHPRCreate is the "gh.pr.create" builtin: `gh pr create` in
// req.WorkDir, spawned through internal/executor/local. Compensable via
// `gh pr close` — 05-gates.md's own confirmation-preview example
// declares exactly this as the effect's revert action.
type GHPRCreate struct {
	Exec local.Executor
}

func (GHPRCreate) Kind() string { return "gh.pr.create" }

// prListEntry is the subset of `gh pr list --json` this provider reads.
type prListEntry struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
}

func (p GHPRCreate) Attempt(ctx context.Context, req Request) (Result, error) {
	branch := req.Args["branch"]
	base := req.Args["base"]
	title := req.Args["title"]
	if branch == "" || base == "" || title == "" {
		return Result{}, fmt.Errorf("gh.pr.create: args[\"branch\"], args[\"base\"], and args[\"title\"] are required")
	}
	argv := []string{"pr", "create", "--base", base, "--head", branch, "--title", title}
	if body := req.Args["body"]; body != "" {
		argv = append(argv, "--body", body)
	} else {
		argv = append(argv, "--body", "")
	}
	code, out, err := runGH(ctx, p.Exec, req, argv, "create")
	if err != nil {
		return Result{}, fmt.Errorf("gh.pr.create: %w", err)
	}
	if code != 0 {
		return Result{Outcome: Failed, Reason: fmt.Sprintf("gh pr create exited %d: %s", code, strings.TrimSpace(out))}, nil
	}
	return Result{Outcome: Applied, ExternalRef: strings.TrimSpace(out)}, nil
}

// Probe looks for an existing open PR from branch — the evidence
// available after a crash mid-`gh pr create`, since a second create
// would open a duplicate. No match found is ok=false (effect.unknown),
// never assumed absent.
func (p GHPRCreate) Probe(ctx context.Context, req Request) (Result, bool, error) {
	branch := req.Args["branch"]
	if branch == "" {
		return Result{}, false, nil
	}
	code, out, err := runGH(ctx, p.Exec, req, []string{"pr", "list", "--head", branch, "--json", "number,url"}, "list")
	if err != nil || code != 0 {
		return Result{}, false, nil
	}
	var entries []prListEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil || len(entries) == 0 {
		return Result{}, false, nil
	}
	return Result{Outcome: Applied, ExternalRef: entries[0].URL}, true, nil
}

// Compensate closes the PR at externalRef — 05-gates.md's declared
// revert for gh.pr.create.
func (p GHPRCreate) Compensate(ctx context.Context, req Request, externalRef string) error {
	code, out, err := runGH(ctx, p.Exec, req, []string{"pr", "close", externalRef}, "close")
	if err != nil {
		return fmt.Errorf("gh.pr.create: compensating (close %s): %w", externalRef, err)
	}
	if code != 0 {
		return fmt.Errorf("gh.pr.create: compensating (close %s) exited %d: %s", externalRef, code, strings.TrimSpace(out))
	}
	return nil
}

func runGH(ctx context.Context, exec local.Executor, req Request, argv []string, subDir string) (int, string, error) {
	dir := filepath.Join(req.Dir, subDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, "", fmt.Errorf("creating scratch dir for gh %v: %w", argv, err)
	}
	bin, err := resolveBinary("gh", req.PathPrefix)
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
		return 0, "", fmt.Errorf("starting gh %v: %w", argv, err)
	}
	res, err := exec.Wait(ctx, started.PID)
	if err != nil {
		return 0, "", fmt.Errorf("waiting for gh %v: %w", argv, err)
	}
	return res.ExitCode, readStdout(dir), nil
}
