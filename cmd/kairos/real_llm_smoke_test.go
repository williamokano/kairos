package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestRealLLMSmoke_Claude is deliberately NOT part of this repository's
// normal test suite: unlike every other test here, it invokes the real
// `claude` CLI for real — real tokens, real wall-clock latency, real
// dependence on the environment's own authentication state. AGENTS.md's
// integration-test discipline ("no containers... an integration test that
// reads the ambient PATH is a flaky test") is why every other actor-kind
// test uses a fake CLI script (writeFakeLLM); this one is the deliberate,
// clearly-labeled exception the user asked for, run once as real evidence
// the harness wiring works end to end — see L22-harness-integration.md's
// "Real end-to-end smoke test" section for what it found the one time it
// was run for real, and how to reproduce that.
//
// Gated behind KAIROS_REAL_LLM_SMOKE=1 so `go test ./...`, `-race`, and CI
// never run it — plain `go test ./cmd/kairos/...` always skips this test.
//
// Re-run it yourself:
//
//	KAIROS_REAL_LLM_SMOKE=1 go test ./cmd/kairos/ -run TestRealLLMSmoke_Claude -v
//
// or `make smoke-llm`, which sets the env var for you. Either way this
// costs real API spend (a few cents) and requires a `claude` binary on
// PATH that is already logged in (`claude /login`) — the test skips,
// rather than fails, if `claude` isn't found on PATH.
func TestRealLLMSmoke_Claude(t *testing.T) {
	if os.Getenv("KAIROS_REAL_LLM_SMOKE") != "1" {
		t.Skip("real-LLM smoke test skipped: set KAIROS_REAL_LLM_SMOKE=1 to run it for real (see L22-harness-integration.md)")
	}
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		t.Skipf("claude CLI not found on PATH, skipping real smoke test: %v", err)
	}

	// A real, already-authenticated claude config dir is required: the
	// daemon overrides HOME to a fresh per-run scratch dir for every LLM
	// node (04-agents.md's per-run HOME isolation), so without
	// CLAUDE_CONFIG_DIR pointed at real credentials the child runs
	// unauthenticated and fails with "Not logged in" — the exact bug this
	// smoke test found live and engine.Config.LLMConfigDir now fixes. Use
	// the real, ambient claude config dir (respecting the same
	// CLAUDE_CONFIG_DIR override a real operator might already be using),
	// defaulting to ~/.claude.
	claudeConfigDir := os.Getenv("CLAUDE_CONFIG_DIR")
	if claudeConfigDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("resolving home dir: %v", err)
		}
		claudeConfigDir = filepath.Join(home, ".claude")
	}
	if _, err := os.Stat(filepath.Join(claudeConfigDir, ".credentials.json")); err != nil {
		t.Skipf("no claude credentials at %s, skipping real smoke test (run `claude /login` first): %v", claudeConfigDir, err)
	}

	bin := buildKairos(t)
	home := t.TempDir()
	t.Cleanup(func() { stopDaemon(t, home) })

	wf, err := filepath.Abs("testdata/real-llm-smoke.yaml")
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	if _, err := os.Stat(wf); err != nil {
		t.Fatalf("fixture missing: %v", err)
	}

	runCmd := exec.Command(bin, "-o", "json", "run", wf)
	runCmd.Env = append(os.Environ(),
		"KAIROS_HOME="+home,
		"KAIROS_LLM_BINARY="+claudeBin,
		"KAIROS_LLM_CONFIG_DIR="+claudeConfigDir,
	)
	runOut, err := runCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("kairos run: %v\n%s", err, runOut)
	}
	t.Logf("kairos run output: %s", runOut)

	var created struct {
		RunID  string `json:"runId"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(runOut, &created); err != nil {
		t.Fatalf("decoding run output %q: %v", runOut, err)
	}
	if created.RunID == "" {
		t.Fatal("expected a non-empty runId")
	}

	// Poll for a terminal Run status — claude may take anywhere from a
	// few seconds to over a minute for a real completion.
	deadline := time.Now().Add(2 * time.Minute)
	var lastShow []byte
	var status string
	for time.Now().Before(deadline) {
		showCmd := exec.Command(bin, "-o", "json", "show", created.RunID)
		showCmd.Env = append(os.Environ(), "KAIROS_HOME="+home)
		out, err := showCmd.CombinedOutput()
		if err != nil {
			t.Fatalf("kairos show: %v\n%s", err, out)
		}
		lastShow = out
		var state struct {
			Status string `json:"Status"`
		}
		if err := json.Unmarshal(out, &state); err != nil {
			t.Fatalf("decoding show output %q: %v", out, err)
		}
		status = state.Status
		if status == "succeeded" || status == "failed" || status == "cancelled" || status == "rejected" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Logf("final kairos show: %s", lastShow)

	if status != "succeeded" {
		t.Fatalf("real claude smoke run ended in status %q (want succeeded); full show output: %s", status, lastShow)
	}

	verifyCmd := exec.Command(bin, "-o", "json", "db", "verify")
	verifyCmd.Env = append(os.Environ(), "KAIROS_HOME="+home)
	verifyOut, err := verifyCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("kairos db verify: %v\n%s", err, verifyOut)
	}
	t.Logf("kairos db verify: %s", verifyOut)
	var report struct {
		MismatchedRunIDs []string `json:"mismatchedRunIds"`
	}
	if err := json.Unmarshal(verifyOut, &report); err != nil {
		t.Fatalf("decoding verify output %q: %v", verifyOut, err)
	}
	if len(report.MismatchedRunIDs) != 0 {
		t.Fatalf("db verify found mismatches: %v", report.MismatchedRunIDs)
	}
}
