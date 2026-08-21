package engine

import (
	"regexp"
	"testing"
)

// TestBuildLLMArgv_claudeInitialInvocation proves NL-29's claude shape
// live-verified against claude 2.1.236 in L22-harness-integration.md:
// --print (headless, never blocks on a terminal), --output-format json
// (this engine reads only the final output.json file, never a stream),
// --permission-mode acceptEdits (verified live not to block a Bash tool
// call), and --session-id carrying THIS engine's own minted id on a
// fresh (non-resume) invocation.
func TestBuildLLMArgv_claudeInitialInvocation(t *testing.T) {
	argv := buildLLMArgv("claude", llmInvocation{sessionID: "abc-123"})

	want := []string{"--print", "--output-format", "json", "--permission-mode", "acceptEdits", "--session-id", "abc-123"}
	assertArgvEqual(t, argv, want)
}

// TestBuildLLMArgv_claudeResumeOmitsSessionID proves --session-id and
// --resume are mutually exclusive in the real invocation: a resume
// carries --resume <id> and never also --session-id.
func TestBuildLLMArgv_claudeResumeOmitsSessionID(t *testing.T) {
	argv := buildLLMArgv("claude", llmInvocation{sessionID: "abc-123", resumeOf: "prior-999"})

	want := []string{"--print", "--output-format", "json", "--permission-mode", "acceptEdits", "--resume", "prior-999"}
	assertArgvEqual(t, argv, want)
	assertArgvExcludes(t, argv, "--session-id")
}

// TestBuildLLMArgv_claudeSessionWorkDirAddsExtraDir is a regression test:
// a `kairos session`-bound run (WorkDirOverride != scratch dir) produced
// NO output.json at all when run for real — Claude Code's own sandbox
// hard-blocks Bash/Read/Write outside its cwd tree, so it could not reach
// KAIROS_OUTPUT's path under the scratch dir once cwd moved to a
// session's worktree elsewhere on disk. --add-dir (verified live via
// `claude --help`) is the fix; extraDir must appear before --resume/
// --session-id, and be entirely absent when there's no override (the
// ordinary, non-session case must not change).
func TestBuildLLMArgv_claudeSessionWorkDirAddsExtraDir(t *testing.T) {
	argv := buildLLMArgv("claude", llmInvocation{sessionID: "abc-123", extraDir: "/scratch/dir"})

	want := []string{"--print", "--output-format", "json", "--permission-mode", "acceptEdits", "--add-dir", "/scratch/dir", "--session-id", "abc-123"}
	assertArgvEqual(t, argv, want)
}

func TestBuildLLMArgv_claudeNoWorkDirOverrideOmitsAddDir(t *testing.T) {
	argv := buildLLMArgv("claude", llmInvocation{sessionID: "abc-123"})
	assertArgvExcludes(t, argv, "--add-dir")
}

// TestBuildLLMArgv_gemini proves gemini's non-interactive shape
// live-verified against gemini 0.22.5: -o json, and critically NO
// positional prompt argument — the prompt stays on stdin (04-agents.md:
// "argv appears in ps for every process on the machine"). gemini's
// --resume only accepts "latest"/an index (gemini --help), never a
// caller-chosen id, so a resumeOf must not leak into gemini's argv.
func TestBuildLLMArgv_gemini(t *testing.T) {
	argv := buildLLMArgv("gemini", llmInvocation{sessionID: "abc-123", resumeOf: "prior-999"})

	want := []string{"-o", "json"}
	assertArgvEqual(t, argv, want)
	assertArgvExcludes(t, argv, "prior-999", "--resume", "--session-id")
}

// TestBuildLLMArgv_opencode proves opencode's non-interactive shape
// live-verified against opencode 1.18.11: `run --format json`, no
// positional message (stdin only, verified live). opencode CAN resume a
// session it minted itself (verified live), but never one this engine
// chooses up front, so nativeResumeSupported is false for it and a
// resumeOf must never appear in its argv.
func TestBuildLLMArgv_opencode(t *testing.T) {
	argv := buildLLMArgv("opencode", llmInvocation{sessionID: "abc-123", resumeOf: "prior-999"})

	want := []string{"run", "--format", "json"}
	assertArgvEqual(t, argv, want)
	assertArgvExcludes(t, argv, "prior-999", "--session")
}

// TestBuildLLMArgv_codexIsSpecOnly documents the honesty discipline for
// the one CLI this environment cannot live-test: codex is not installed
// here, so this only proves the builder matches 04-agents.md's written
// spec, not real CLI behaviour (see L22-harness-integration.md).
func TestBuildLLMArgv_codexIsSpecOnly(t *testing.T) {
	argv := buildLLMArgv("codex", llmInvocation{schemaPath: "/s.json", outputPath: "/o.json"})
	want := []string{"exec", "--json", "--sandbox", "workspace-write", "--output-schema", "/s.json", "--output-last-message", "/o.json"}
	assertArgvEqual(t, argv, want)

	resumeArgv := buildLLMArgv("codex", llmInvocation{resumeOf: "prior-999"})
	assertArgvEqual(t, resumeArgv, []string{"exec", "resume", "prior-999"})
}

// TestBuildLLMArgv_unknownKindGetsNoExtraFlags proves "local" (L08's
// placeholder CLI kind) and any other unregistered actor kind keep this
// repo's pre-NL-29 behaviour: no extra flags at all, only the file
// contract via env vars — so a fake-CLI test actor is unaffected by any
// of the above.
func TestBuildLLMArgv_unknownKindGetsNoExtraFlags(t *testing.T) {
	if argv := buildLLMArgv("local", llmInvocation{sessionID: "abc-123"}); len(argv) != 0 {
		t.Errorf("buildLLMArgv(local) = %v, want empty", argv)
	}
	if argv := buildLLMArgv("some-future-cli", llmInvocation{}); len(argv) != 0 {
		t.Errorf("buildLLMArgv(unknown) = %v, want empty", argv)
	}
}

// TestConfigDirEnv is the regression test for the real bug
// L22-harness-integration.md's "real end-to-end smoke test" section found
// live: dispatchLLMActor sets HOME to a fresh per-run scratch dir (by
// design, 04-agents.md), which means an authenticated CLI that stores
// credentials under $HOME (claude's ~/.claude.json/~/.claude/.credentials.json)
// finds none there and fails with "Not logged in" — verified live against
// the real claude 2.1.236 binary. configDirEnv is the fix: it hands the
// child the one env var 04-agents.md itself documents for exactly this
// (CLAUDE_CONFIG_DIR / CODEX_HOME), pointed at a persistent, pre-authenticated
// directory instead of the ephemeral per-run HOME.
func TestConfigDirEnv(t *testing.T) {
	tests := []struct {
		name      string
		actorKind string
		configDir string
		want      []string
	}{
		{"claude gets CLAUDE_CONFIG_DIR", "claude", "/home/u/.claude", []string{"CLAUDE_CONFIG_DIR=/home/u/.claude"}},
		{"codex gets CODEX_HOME", "codex", "/home/u/.codex", []string{"CODEX_HOME=/home/u/.codex"}},
		{"gemini has no known var", "gemini", "/home/u/.gemini", nil},
		{"opencode has no known var", "opencode", "/home/u/.opencode", nil},
		{"empty configDir emits nothing even for claude", "claude", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := configDirEnv(tt.actorKind, tt.configDir)
			if len(got) != len(tt.want) {
				t.Fatalf("configDirEnv(%q, %q) = %v, want %v", tt.actorKind, tt.configDir, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("configDirEnv(%q, %q)[%d] = %q, want %q", tt.actorKind, tt.configDir, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestNativeResumeSupported(t *testing.T) {
	tests := []struct {
		kind string
		want bool
	}{
		{"claude", true},
		{"codex", true},
		{"gemini", false},
		{"opencode", false},
		{"local", false},
	}
	for _, tt := range tests {
		if got := nativeResumeSupported(tt.kind); got != tt.want {
			t.Errorf("nativeResumeSupported(%q) = %v, want %v", tt.kind, got, tt.want)
		}
	}
}

// uuidShape matches claude's own validation shape (8-4-4-4-12 lowercase
// hex), verified live: `claude -p --session-id not-a-uuid` is rejected
// with "Error: Invalid session ID. Must be a valid UUID", while the same
// dash-hex shape (even with non-RFC4122 version/variant bits, since this
// engine derives it from ULID bytes rather than true randomness) is
// accepted.
var uuidShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// TestNewSessionID_isUUIDShaped is the regression test for the real bug
// this pass fixed: minting session ids via ulid.Make().String() (26-char
// Crockford base32, e.g. "01ARZ3NDEKTSV4RRFFQ69G5FAV") is never
// UUID-shaped and claude's real --session-id flag would always have
// rejected it.
func TestNewSessionID_isUUIDShaped(t *testing.T) {
	id := newSessionID()
	if !uuidShape.MatchString(id) {
		t.Errorf("newSessionID() = %q, want UUID-shaped (8-4-4-4-12 hex)", id)
	}
}

func TestNewSessionID_producesDistinctIDs(t *testing.T) {
	first := newSessionID()
	second := newSessionID()
	if first == second {
		t.Errorf("two calls to newSessionID() both produced %q", first)
	}
}

func assertArgvEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv = %v, want %v", got, want)
		}
	}
}

func assertArgvExcludes(t *testing.T, argv []string, excluded ...string) {
	t.Helper()
	for _, e := range excluded {
		for _, a := range argv {
			if a == e {
				t.Errorf("argv %v unexpectedly contains %q", argv, e)
			}
		}
	}
}
