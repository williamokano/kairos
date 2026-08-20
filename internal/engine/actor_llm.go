package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/oklog/ulid/v2"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/executor/local"
	"github.com/williamokano/kairos/internal/registry"
)

// dispatchLLMActor maps actor kinds claude/codex/gemini/local —
// 04-agents.md's "an LLM CLI" — onto a single configured CLI binary
// (Config.LLMBinary), invoked through the file contract: KAIROS_OUTPUT/
// KAIROS_SCHEMA env vars, prompt on stdin (never argv — "argv appears in
// ps for every process on the machine, and prompts routinely contain
// issue bodies and file excerpts"). Real per-CLI flag probing
// (--session-id, --output-format stream-json, --permission-mode, ...) is
// Future work: this reads only the CLI's final output.json, never an
// incremental stream (documented decision — see L08-actor-sdk.md).
func (e *Engine) dispatchLLMActor(ctx context.Context, nd registry.NodeDef, c domain.CmdStartNode, actorKind string) error {
	if e.llmBinary == "" {
		return e.appendNodeFailed(ctx, c.RunID, c.NodeID, c.ExecID, domain.FailFailure,
			fmt.Sprintf("actor %q requires a configured LLM binary (engine.Config.LLMBinary is empty)", actorKind))
	}

	dir := e.scratchDir(c.RunID, c.ExecID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return e.appendNodeFailed(ctx, c.RunID, c.NodeID, c.ExecID, domain.FailFailure, "creating scratch dir: "+err.Error())
	}
	outputPath := filepath.Join(dir, "output.json")
	schemaPath := filepath.Join(dir, "output.schema.json")
	if err := os.WriteFile(schemaPath, nd.OutputSchemaRaw, 0o444); err != nil {
		return e.appendNodeFailed(ctx, c.RunID, c.NodeID, c.ExecID, domain.FailFailure, "writing schema file: "+err.Error())
	}

	workDir := dir
	if nd.Workspace == registry.WorkspaceWrite {
		if e.workspaceRepo == "" {
			return e.appendNodeFailed(ctx, c.RunID, c.NodeID, c.ExecID, domain.FailFailure,
				"node declares workspace: write but the engine has no configured WorkspaceRepo")
		}
		ws, err := e.workspaces.Provision(ctx, c.RunID, e.workspaceRepo)
		if err != nil {
			return e.appendNodeFailed(ctx, c.RunID, c.NodeID, c.ExecID, domain.FailFailure, "provisioning workspace: "+err.Error())
		}
		workDir = ws.Dir
	}

	sessionID, resumeOf, err := e.resolveSession(ctx, nd, c, workDir)
	if err != nil {
		return fmt.Errorf("resolving session: %w", err)
	}
	if err := e.appendNext(ctx, c.RunID, domain.LLMSessionStarted{
		RunID: c.RunID, NodeID: c.NodeID, ExecID: c.ExecID,
		SessionID: sessionID, Resumed: resumeOf != "", Dir: workDir,
	}); err != nil {
		return err
	}

	started, err := e.startLLM(ctx, c, workDir, dir, outputPath, schemaPath, nd.Prompt, resumeOf)
	if err != nil {
		return e.appendNodeFailed(ctx, c.RunID, c.NodeID, c.ExecID, domain.FailFailure, "starting process: "+err.Error())
	}
	if err := e.appendNext(ctx, c.RunID, domain.NodeExecutionStarted(c)); err != nil {
		return err
	}

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.reapLLM(ctx, c, workDir, dir, outputPath, schemaPath, started.PID)
	}()
	return nil
}

// resolveSession implements 04-agents.md's "two concepts, never
// conflated" table: a Kairos Session (our ULID) is minted on the first
// attempt. A later attempt with SessionAffinity node/run tries to resume
// the prior attempt's session — UNLESS the path-keying trap applies: the
// prior session's Dir no longer exists (a fresh workspace, or one this
// attempt's own retry.mutate/freshWorkspace deliberately changed), in
// which case resuming would silently find nothing, so SessionResumeFailed
// is recorded and a fresh session is minted instead of ever attempting
// the resume. SessionAffinity == "" or "execution" always mints fresh —
// per-attempt isolation is the point of that setting.
func (e *Engine) resolveSession(ctx context.Context, nd registry.NodeDef, c domain.CmdStartNode, workDir string) (sessionID, resumeOf string, err error) {
	if c.Attempt <= 1 || nd.SessionAffinity == "" || nd.SessionAffinity == "execution" {
		return ulid.Make().String(), "", nil
	}

	prior, ok, err := e.priorSession(ctx, c.RunID, c.NodeID)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return ulid.Make().String(), "", nil
	}
	if _, statErr := os.Stat(prior.Dir); statErr != nil {
		if err := e.appendNext(ctx, c.RunID, domain.SessionResumeFailed{
			RunID: c.RunID, NodeID: c.NodeID, ExecID: c.ExecID, PriorSessionID: prior.SessionID,
		}); err != nil {
			return "", "", err
		}
		return ulid.Make().String(), "", nil
	}
	if prior.Dir != workDir {
		// Same trap, caught the other direction: the recorded dir still
		// exists, but this attempt's own workDir isn't it (a fresh
		// workspace was provisioned for this attempt) — resuming against
		// the wrong cwd is exactly the silent-miss 04-agents.md warns
		// about, so treat it identically: record and mint fresh.
		if err := e.appendNext(ctx, c.RunID, domain.SessionResumeFailed{
			RunID: c.RunID, NodeID: c.NodeID, ExecID: c.ExecID, PriorSessionID: prior.SessionID,
		}); err != nil {
			return "", "", err
		}
		return ulid.Make().String(), "", nil
	}
	return prior.SessionID, prior.SessionID, nil
}

// priorSession scans nodeID's own prior LLMSessionStarted facts in runID's
// stream for the most recent one — the engine's only source of truth for
// "what session did the last attempt use", since NodeExecution itself
// carries no session field (domain doesn't know sessions exist).
func (e *Engine) priorSession(ctx context.Context, runID, nodeID string) (domain.LLMSessionStarted, bool, error) {
	envs, err := e.store.Read(ctx, runID)
	if err != nil {
		return domain.LLMSessionStarted{}, false, fmt.Errorf("reading stream %s: %w", runID, err)
	}
	var latest domain.LLMSessionStarted
	found := false
	for _, env := range envs {
		s, ok := env.Event.(domain.LLMSessionStarted)
		if !ok || s.NodeID != nodeID {
			continue
		}
		latest = s
		found = true
	}
	return latest, found, nil
}

func (e *Engine) startLLM(ctx context.Context, c domain.CmdStartNode, workDir, dir, outputPath, schemaPath, prompt, resumeOf string) (local.Started, error) {
	env := []string{
		"HOME=" + dir,
		"PATH=/usr/bin:/bin:/usr/local/bin",
		"TERM=dumb", "NO_COLOR=1", "CI=1", "GIT_TERMINAL_PROMPT=0",
		"KAIROS_RUN_ID=" + c.RunID,
		"KAIROS_NODE_ID=" + c.NodeID,
		"KAIROS_NODE_EXEC_ID=" + c.ExecID,
		"KAIROS_DIR=" + dir,
		"KAIROS_OUTPUT=" + outputPath,
		"KAIROS_SCHEMA=" + schemaPath,
	}
	if resumeOf != "" {
		env = append(env, "KAIROS_RESUME_SESSION_ID="+resumeOf)
	}
	return e.exec.Start(ctx, local.ExecSpec{
		RunID: c.RunID, NodeID: c.NodeID, ExecID: c.ExecID,
		Dir:     dir,
		WorkDir: workDir,
		Env:     env,
		Argv:    []string{e.llmBinary},
		Stdin:   []byte(prompt),
	})
}

// reapLLM blocks for the process's exit, then runs 04-agents.md's Stage 2
// repair turn (one bounded in-session repair attempt, only) before
// finalising via the same NodeOutputReceived path dispatchShellActor
// uses — Stage 3 (constrained-decoding extraction) and Stage 4 (a fresh
// top-level attempt, possibly on a mutated actor) are NOT this function's
// job: Stage 4 is domain's own existing retry ladder, reached simply by
// this function reporting SchemaValid: false like any other actor.
func (e *Engine) reapLLM(ctx context.Context, c domain.CmdStartNode, workDir, dir, outputPath, schemaPath string, pid int) {
	res, err := e.exec.Wait(ctx, pid)
	if err != nil {
		_ = e.appendNodeFailed(ctx, c.RunID, c.NodeID, c.ExecID, domain.FailFailure, "waiting for process: "+err.Error())
		return
	}
	if res.ExitCode != 0 {
		_ = e.appendNodeFailed(ctx, c.RunID, c.NodeID, c.ExecID, domain.FailFailure, fmt.Sprintf("exit status %d", res.ExitCode))
		return
	}

	valid, violations := e.checkOutput(outputPath, schemaPath)
	if !valid {
		if err := e.appendNext(ctx, c.RunID, domain.OutputRepairAttempted{
			RunID: c.RunID, NodeID: c.NodeID, ExecID: c.ExecID, Errors: violations,
		}); err != nil {
			e.log.Error("recording output.repair.attempted", "error", err)
		}
		e.repairTurn(ctx, c, workDir, dir, outputPath, schemaPath, violations)
		valid, _ = e.checkOutput(outputPath, schemaPath)
	}

	// This actor's own reported cost is never parsed (no stream parsing —
	// documented decision), so cost is always "unknown", per
	// 04-agents.md's third accounting tier, honestly rather than silently.
	if err := e.appendNext(ctx, c.RunID, domain.SessionCostUnavailable{RunID: c.RunID, NodeID: c.NodeID, ExecID: c.ExecID}); err != nil {
		e.log.Error("recording session.cost.unavailable", "error", err)
	}

	body, readErr := os.ReadFile(outputPath)
	if readErr != nil {
		_ = e.appendNext(ctx, c.RunID, domain.NodeOutputReceived{RunID: c.RunID, NodeID: c.NodeID, ExecID: c.ExecID, SchemaValid: false})
		return
	}
	_ = e.appendNext(ctx, c.RunID, domain.NodeOutputReceived{
		RunID: c.RunID, NodeID: c.NodeID, ExecID: c.ExecID, SchemaValid: valid, Output: json.RawMessage(body),
	})
}

// repairTurn runs the CLI exactly once more, in the same workDir/session,
// with the validation errors appended to a fix-only instruction —
// "one attempt only: a second means the schema or the node is wrong"
// (04-agents.md). Errors on this second spawn are swallowed: whatever
// happened, checkOutput's second call after this returns is what decides
// the outcome, matching Stage 2's contract exactly (repair or don't, the
// eventual NodeOutputReceived carries the truth either way).
func (e *Engine) repairTurn(ctx context.Context, c domain.CmdStartNode, workDir, dir, outputPath, schemaPath string, violations []string) {
	var b bytes.Buffer
	b.WriteString("Your output does not validate:\n")
	for _, v := range violations {
		b.WriteString("  " + v + "\n")
	}
	b.WriteString("Fix the FILE only. Change no code. Run `kairos check-output` until it exits 0.\n")

	started, err := e.startLLM(ctx, c, workDir, dir, outputPath, schemaPath, b.String(), "")
	if err != nil {
		return
	}
	_, _ = e.exec.Wait(ctx, started.PID)
}

// checkOutput reuses registry.ValidateFile — the exact function `kairos
// check-output` calls — rather than the shell actor's in-memory
// validateOutput, since only the file-based validator also produces the
// json-pointer violation lines the repair turn's prompt needs.
func (e *Engine) checkOutput(outputPath, schemaPath string) (bool, []string) {
	valid, violations, err := registry.ValidateFile(outputPath, schemaPath)
	if err != nil {
		return false, []string{fmt.Sprintf("/: %s", err)}
	}
	return valid, violations
}
