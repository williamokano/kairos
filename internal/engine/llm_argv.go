package engine

// llm_argv.go builds the per-actor-kind CLI invocation NL-29 asked for
// (L08-actor-sdk.md's Future work, closed by L22-harness-integration.md):
// real flags per CLI kind, instead of a bare binary invocation carrying
// only the KAIROS_OUTPUT/KAIROS_SCHEMA env vars and a stdin prompt. Every
// shape below stays inside 04-agents.md's non-negotiables: the prompt is
// NEVER on argv — it stays on stdin, unchanged — and this engine still
// reads only the final output.json file; none of these flags ask a CLI
// for an incremental stream this engine would then have to parse (that
// stays Future work — see reapLLM's doc comment).
//
// Verified live, for real, in this environment (see
// L22-harness-integration.md for the exact commands run):
//   - claude   2.1.236
//   - gemini   0.22.5
//   - opencode 1.18.11
//
// codex is NOT installed in this environment. codexArgv is transcribed
// only from 04-agents.md's documented spec (`--sandbox workspace-write`)
// and is UNVERIFIED — treat it as spec-only, not tested, until it can be
// run against a real codex binary.

// llmInvocation carries what a per-kind argv builder needs to decide its
// flags. sessionID is this engine's own minted identifier for the
// session about to start; resumeOf is non-empty only when THIS
// invocation should resume an existing native session (only ever set
// when nativeResumeSupported(actorKind) is true — see below).
// schemaPath/outputPath are the same two files every actor kind already
// gets via KAIROS_SCHEMA/KAIROS_OUTPUT; codex is the one kind whose CLI
// takes them as flags too (constrained decoding, 04-agents.md's Stage 0).
type llmInvocation struct {
	sessionID  string
	resumeOf   string
	schemaPath string
	outputPath string
}

// llmArgvBuilders maps an actor kind onto its CLI's real, current
// non-interactive invocation shape — a small table, not a growing
// if-chain, so adding a new CLI kind is one map entry plus one function.
var llmArgvBuilders = map[string]func(llmInvocation) []string{
	"claude":   claudeArgv,
	"gemini":   geminiArgv,
	"opencode": opencodeArgv,
	"codex":    codexArgv,
}

// buildLLMArgv returns actorKind's flags, never including the binary
// itself (startLLM prepends that). Unknown kinds — including "local",
// L08's placeholder CLI kind with no real invocation to shape — get no
// extra flags: exactly this repo's pre-NL-29 behaviour, so a fake-CLI
// test actor keeps working unchanged.
func buildLLMArgv(actorKind string, inv llmInvocation) []string {
	if b, ok := llmArgvBuilders[actorKind]; ok {
		return b(inv)
	}
	return nil
}

// nativeResumeSupported reports whether actorKind's CLI can resume a
// session BY AN ID THIS ENGINE CHOSE — the specific capability
// 04-agents.md's "native" resume mode requires. claude accepts
// --session-id on a fresh invocation and --resume <that id> later, both
// verified live: a second `claude -p --resume $SID` recalled a fact only
// the first invocation had been told. codex is documented (04-agents.md)
// as accepting the same shape via `exec resume <id>` — UNVERIFIED here,
// codex is not installed in this environment. gemini's --resume only
// accepts "latest" or a numeric list index (gemini --help), never an
// arbitrary caller-chosen id. opencode's --session DOES resume a
// specific id (verified live: a second, separate `opencode run --session
// <id>` recalled a fact from the first) — but only an id OPENCODE
// ITSELF mints on its first run and reports back inside the JSON event
// stream this engine deliberately never parses (file-contract only, see
// reapLLM's doc comment); this engine has no id to hand opencode at the
// point a FIRST invocation is built, so opencode is honestly left out of
// this set rather than wired to a capability the engine can't actually
// supply.
func nativeResumeSupported(actorKind string) bool {
	return actorKind == "claude" || actorKind == "codex"
}

// claudeArgv: --print is Claude Code's headless flag ("starts an
// interactive session by default, use -p/--print for non-interactive
// output" — claude --help); without it, claude blocks on a terminal that
// is never there under kairos, which is exactly the hang NL-29 exists to
// avoid. --output-format json: this engine reads only the final
// output.json file (documented decision, see reapLLM), so stream-json
// buys nothing here and would need a parser this engine deliberately
// doesn't have — plain "json" is the single-result shape. --permission-
// mode acceptEdits: verified live — with this flag, a Bash tool call
// completed with `"permission_denials":[]` and no prompt at all; with no
// --permission-mode flag at all, claude -p still runs but a tool call
// needing approval has nothing to answer it under kairos (no tty), which
// is the same hang. --session-id / --resume are mutually exclusive on
// the real CLI (claude --help lists them as separate flags, and passing
// a fresh --session-id while also resuming makes no sense), so exactly
// one of the two is ever emitted here, never both.
func claudeArgv(inv llmInvocation) []string {
	argv := []string{"--print", "--output-format", "json", "--permission-mode", "acceptEdits"}
	if inv.resumeOf != "" {
		return append(argv, "--resume", inv.resumeOf)
	}
	return append(argv, "--session-id", inv.sessionID)
}

// geminiArgv: no positional prompt — verified live that gemini reads
// stdin as the prompt when no positional query is given (`echo ... |
// gemini -o json` answered the piped text), so the prompt stays off
// argv exactly like every other actor kind (04-agents.md: "argv appears
// in ps for every process on the machine"). -o json is gemini's
// non-interactive structured-result flag (gemini --help: "-o,
// --output-format ... choices: text, json, stream-json"); json rather
// than stream-json for the same "we don't parse a stream" reason as
// claude above. gemini's own --resume only accepts "latest" or a
// numeric index (gemini --help), never a caller-chosen id, so
// inv.resumeOf is never consulted here — nativeResumeSupported already
// reports false for gemini, so this function is never asked to add one.
func geminiArgv(llmInvocation) []string {
	return []string{"-o", "json"}
}

// opencodeArgv: `run` is opencode's one-shot, non-interactive verb
// (opencode --help); it reads the message from stdin when no positional
// message is given (verified live: `echo ... | opencode run --format
// json` answered the piped prompt), keeping the prompt off argv exactly
// like claude/gemini above. --format json emits one JSON object per step
// on stdout instead of opencode's default human-rendered output — a file
// write via its Write tool proceeded with no permission prompt in this
// same non-interactive mode, verified live, so no --auto flag is needed
// to avoid a hang. opencode DOES support resuming one specific session
// (verified live: a second, separate `opencode run --session <id>`
// recalled a fact stated in an earlier invocation), but only an id
// opencode itself minted and reported back inside the JSON event stream
// — never a caller-chosen one — so there is nothing for THIS engine to
// pass as --session on a first invocation; nativeResumeSupported (above)
// never asks this function to add one.
func opencodeArgv(llmInvocation) []string {
	return []string{"run", "--format", "json"}
}

// codexArgv is UNVERIFIED in this environment: codex is not installed
// here (see L22-harness-integration.md), so this is transcribed only
// from 04-agents.md's documented invocation shape
// (`codex exec --json --sandbox workspace-write --output-schema ...`),
// never actually run against a real binary. `exec` is codex's
// non-interactive verb; `--json` its structured-event flag; `--sandbox
// workspace-write` is the Seatbelt/Landlock enforcement 04-agents.md
// calls "the single best guardrail available in this variant"; process
// cwd is set via the same ExecSpec.WorkDir every other actor kind
// already uses, so no --cd flag is needed to match the doc's `--cd`
// example. --output-schema and --output-last-message point codex at the
// SAME schemaPath/outputPath every other kind already receives via
// KAIROS_SCHEMA/KAIROS_OUTPUT, layering codex's own constrained decoding
// (04-agents.md's Stage 0 — "Done; no repair path exists") directly on
// top of this engine's file contract, at no extra cost. Native resume is
// `exec resume <id>` per 04-agents.md, kept identical to this repo's
// prior (pre-NL-29) codex resume shape.
func codexArgv(inv llmInvocation) []string {
	if inv.resumeOf != "" {
		return []string{"exec", "resume", inv.resumeOf}
	}
	return []string{
		"exec", "--json",
		"--sandbox", "workspace-write",
		"--output-schema", inv.schemaPath,
		"--output-last-message", inv.outputPath,
	}
}
