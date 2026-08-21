package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/oklog/ulid/v2"

	"github.com/williamokano/kairos/internal/conversation"
	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/executor/local"
	"github.com/williamokano/kairos/internal/registry"
)

// dispatchLLMActor maps actor kinds claude/codex/gemini/opencode/local —
// 04-agents.md's "an LLM CLI" — onto a single configured CLI binary
// (Config.LLMBinary), invoked through the file contract: KAIROS_OUTPUT/
// KAIROS_SCHEMA env vars, prompt on stdin (never argv — "argv appears in
// ps for every process on the machine, and prompts routinely contain
// issue bodies and file excerpts"), PLUS the real per-CLI flags
// llm_argv.go builds (--session-id/--resume, --output-format,
// --permission-mode, ... — NL-29, closed by
// L22-harness-integration.md). This still reads only the CLI's final
// output.json, never an incremental stream (documented decision — see
// L08-actor-sdk.md and reapLLM below).
func (e *Engine) dispatchLLMActor(ctx context.Context, nd registry.NodeDef, c domain.CmdStartNode, actorKind string) error {
	// See dispatchShellActor's identical comment: releases c.ExecID's
	// admission claim on every synchronous failure path below; spawned
	// marks the point past which reapLLM owns that release instead.
	spawned := false
	defer func() {
		if !spawned {
			e.releaseAndDrain(ctx, c.ExecID)
		}
	}()

	if e.llmBinary == "" {
		return e.startThenFail(ctx, c, domain.FailFailure,
			fmt.Sprintf("actor %q requires a configured LLM binary (engine.Config.LLMBinary is empty)", actorKind))
	}

	dir := e.scratchDir(c.RunID, c.ExecID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return e.startThenFail(ctx, c, domain.FailFailure, "creating scratch dir: "+err.Error())
	}
	outputPath := filepath.Join(dir, "output.json")
	schemaPath := filepath.Join(dir, "output.schema.json")
	if err := os.WriteFile(schemaPath, nd.OutputSchemaRaw, 0o444); err != nil {
		return e.startThenFail(ctx, c, domain.FailFailure, "writing schema file: "+err.Error())
	}

	workDir := dir
	if nd.Workspace == registry.WorkspaceWrite {
		if e.workspaceRepo == "" {
			return e.startThenFail(ctx, c, domain.FailFailure,
				"node declares workspace: write but the engine has no configured WorkspaceRepo")
		}
		ws, err := e.workspaces.Provision(ctx, c.RunID, e.workspaceRepo)
		if err != nil {
			return e.startThenFail(ctx, c, domain.FailFailure, "provisioning workspace: "+err.Error())
		}
		workDir = ws.Dir
	}

	sessionID, resumeOf, err := e.resolveSession(ctx, nd, c, workDir)
	if err != nil {
		return fmt.Errorf("resolving session: %w", err)
	}
	resumeMode := ""
	if resumeOf != "" {
		resumeMode = "native"
	}
	if err := e.appendNext(ctx, c.RunID, domain.LLMSessionStarted{
		RunID: c.RunID, NodeID: c.NodeID, ExecID: c.ExecID,
		SessionID: sessionID, Resumed: resumeOf != "", Dir: workDir, ResumeMode: resumeMode,
	}); err != nil {
		return err
	}

	started, err := e.startLLM(ctx, c, actorKind, workDir, dir, outputPath, schemaPath, nd.Prompt, sessionID, resumeOf)
	if err != nil {
		return e.startThenFail(ctx, c, domain.FailFailure, "starting process: "+err.Error())
	}
	if err := e.appendNext(ctx, c.RunID, domain.NodeExecutionStarted(c)); err != nil {
		return err
	}

	spawned = true
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.reapLLM(ctx, c, actorKind, workDir, dir, outputPath, schemaPath, sessionID, started.PID, nd.Workspace == registry.WorkspaceWrite,
			nd.PostOutputToConversation, nd.ConversationRunOverride)
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
	// nd.ResumeSessionID is `kairos do`'s continuation escape hatch: a
	// prior session lives on a DIFFERENT run (a fresh run is synthesized
	// per chat turn — see adhoc.go's doc comment for why), so the normal
	// same-run/same-node priorSession lookup below can never find it —
	// it only ever scans this run's own event stream. Trust the explicit
	// declaration outright rather than re-deriving it: it was set
	// moments ago by the same request that's about to dispatch this
	// node, not stale author-time config, and if the resume target has
	// genuinely gone (workspace deleted, process long dead) the LLM
	// CLI's own --resume will fail loudly and the node fails honestly —
	// exactly AGENTS.md rule 1's "no silent failure," not a new one.
	if nd.ResumeSessionID != "" {
		return nd.ResumeSessionID, nd.ResumeSessionID, nil
	}
	if c.Attempt <= 1 || nd.SessionAffinity == "" || nd.SessionAffinity == "execution" {
		return newSessionID(), "", nil
	}

	prior, ok, err := e.priorSession(ctx, c.RunID, c.NodeID)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return newSessionID(), "", nil
	}
	if _, statErr := os.Stat(prior.Dir); statErr != nil {
		if err := e.appendNext(ctx, c.RunID, domain.SessionResumeFailed{
			RunID: c.RunID, NodeID: c.NodeID, ExecID: c.ExecID, PriorSessionID: prior.SessionID,
		}); err != nil {
			return "", "", err
		}
		return newSessionID(), "", nil
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
		return newSessionID(), "", nil
	}
	return prior.SessionID, prior.SessionID, nil
}

// newSessionID mints kairos's own opaque session identifier: a ULID's 16
// time-ordered/random bytes, re-rendered in UUID shape (8-4-4-4-12 hex
// groups) rather than returned via ulid.Make().String()'s own
// Crockford-base32 encoding. This is a real correctness fix, not
// cosmetic: Claude Code's --session-id flag validates its argument's
// SHAPE and rejects anything that isn't UUID-formatted — verified live,
// `claude -p --session-id not-a-uuid` fails with "Error: Invalid session
// ID. Must be a valid UUID" — and a ULID's own string form (26
// Crockford32 characters, no dashes) is never that shape, so passing it
// as --session-id would always have been rejected by the real CLI. The
// ULID's time-ordering property survives because these are the same 16
// bytes, only re-rendered; ulid.Make() is kept (rather than
// crypto/rand.Read directly) so this stays monotonic like every other
// ULID this engine mints, and needs no new dependency.
func newSessionID() string {
	id := ulid.Make()
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16])
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

// startLLM spawns actorKind's CLI with llm_argv.go's real per-kind flags
// (never the prompt — that stays on stdin, per the file contract). Only
// a resumeOf that nativeResumeSupported(actorKind) actually honours ever
// reaches the child as a resume flag; a resumeOf the CLI can't act on
// (e.g. gemini, opencode on a first invocation) is still recorded via
// KAIROS_RESUME_SESSION_ID as Kairos's own audit trail of what it asked
// for, even though the argv builder for that kind ignores it.
func (e *Engine) startLLM(ctx context.Context, c domain.CmdStartNode, actorKind, workDir, dir, outputPath, schemaPath, prompt, sessionID, resumeOf string) (local.Started, error) {
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
	env = append(env, configDirEnv(actorKind, e.llmConfigDir)...)
	argv := append([]string{e.llmBinary}, buildLLMArgv(actorKind, llmInvocation{
		sessionID:  sessionID,
		resumeOf:   resumeOf,
		schemaPath: schemaPath,
		outputPath: outputPath,
	})...)
	return e.exec.Start(ctx, local.ExecSpec{
		RunID: c.RunID, NodeID: c.NodeID, ExecID: c.ExecID,
		Dir:     dir,
		WorkDir: workDir,
		Env:     env,
		Argv:    argv,
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
func (e *Engine) reapLLM(ctx context.Context, c domain.CmdStartNode, actorKind, workDir, dir, outputPath, schemaPath, sessionID string, pid int, isWorkspaceWrite, postToConversation bool, conversationRunOverride string) {
	defer e.releaseAndDrain(ctx, c.ExecID)
	defer e.collectLogs(ctx, c.RunID, c.NodeID, c.ExecID, dir)

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
		e.repairTurn(ctx, c, actorKind, workDir, dir, outputPath, schemaPath, sessionID, violations)
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
	inline, ref, err := e.storeOutput(body)
	if err != nil {
		_ = e.appendNodeFailed(ctx, c.RunID, c.NodeID, c.ExecID, domain.FailFailure, "storing output artifact: "+err.Error())
		return
	}
	var raw json.RawMessage
	if inline != nil {
		raw = json.RawMessage(inline)
	}
	if err := e.appendNext(ctx, c.RunID, domain.NodeOutputReceived{
		RunID: c.RunID, NodeID: c.NodeID, ExecID: c.ExecID, SchemaValid: valid, Output: raw, OutputRef: ref,
	}); err == nil && valid {
		e.maybeSnapshotWorkspace(ctx, isWorkspaceWrite, c.RunID, c.NodeID, c.ExecID)
		if postToConversation {
			target := conversationRunOverride
			if target == "" {
				target = c.RunID
			}
			if err := conversation.AppendMessage(ctx, e.store, target, "assistant", extractReplyText(raw)); err != nil {
				e.log.Error("posting node output to conversation", "runID", c.RunID, "target", target, "error", err)
			}
		}
	}
}

// extractReplyText turns a node's JSON output into chat-thread prose —
// `kairos do`'s narrow heuristic (result -> message -> text -> the whole
// object, stringified), not a general schema-to-prose renderer: the
// synthesized ad hoc definition always declares {result: "string!"}, so
// the first branch is what actually fires in practice; the fallbacks
// exist so a hand-authored workflow that sets postOutputToConversation
// against a differently-shaped schema still gets SOMETHING readable
// rather than an empty message.
func extractReplyText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err == nil {
		for _, key := range []string{"result", "message", "text"} {
			if v, ok := fields[key]; ok {
				var s string
				if err := json.Unmarshal(v, &s); err == nil {
					return s
				}
			}
		}
	}
	return string(raw)
}

// repairTurn runs the CLI exactly once more, in the same workDir, with
// the validation errors appended to a fix-only instruction — "one
// attempt only: a second means the schema or the node is wrong"
// (04-agents.md). Errors on this second spawn are swallowed: whatever
// happened, checkOutput's second call after this returns is what decides
// the outcome, matching Stage 2's contract exactly (repair or don't, the
// eventual NodeOutputReceived carries the truth either way).
//
// "In the same ... session" (04-agents.md's Stage 2) is now real, not
// aspirational: for a kind nativeResumeSupported reports true for
// (claude, codex), this repair invocation resumes sessionID — the same
// session the failing attempt just ran in — via --resume, so the repair
// turn sees the model's own prior turn and just fixes the file, exactly
// as `claude -p --resume $SID` in 04-agents.md's Stage 2 example. For a
// kind that can't resume by a caller-chosen id (gemini, opencode, local),
// this is a fresh invocation with no continuity — the same behaviour
// this function always had, before native resume was wired for real.
func (e *Engine) repairTurn(ctx context.Context, c domain.CmdStartNode, actorKind, workDir, dir, outputPath, schemaPath, sessionID string, violations []string) {
	var b bytes.Buffer
	b.WriteString("Your output does not validate:\n")
	for _, v := range violations {
		b.WriteString("  " + v + "\n")
	}
	b.WriteString("Fix the FILE only. Change no code. Run `kairos check-output` until it exits 0.\n")

	resumeOf := ""
	if nativeResumeSupported(actorKind) {
		resumeOf = sessionID
	}
	started, err := e.startLLM(ctx, c, actorKind, workDir, dir, outputPath, schemaPath, b.String(), sessionID, resumeOf)
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
