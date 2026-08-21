package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// waivableGateDefPath mirrors internal/engine/gates_waiver_test.go's own
// fixture exactly (a `command` gate that always fails, `waivable: true`,
// `maxIterationsPerNode: 1`) — reused here rather than reinvented, since
// that file's TestEngine_waivableTrueGateFailureCanBeWaived already
// proves the underlying engine.GrantWaiver mechanics; this test's job is
// only to prove the CLI/API plumbing on top of it works against a real
// daemon and a real binary.
func waivableGateDefPath(t *testing.T) string {
	t.Helper()
	const yaml = `
name: waivable-true
limits:
  loopGuard: { maxIterationsPerNode: 1, onExceeded: escalate-to-human }
gates:
  guardrails:
    kind: command
    waivable: true
    check:
      command: ["false"]
      expect: { exitCode: 0 }
nodes:
  - id: n1
    actor: shell
    prompt: "sleep 1 && echo '{\"ok\":true}' > \"$KAIROS_OUTPUT_PATH\""
    output: { ok: "bool!" }
    gates: [guardrails]
`
	defPath := filepath.Join(t.TempDir(), "def.yaml")
	if err := os.WriteFile(defPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}
	return defPath
}

// TestIntegration_waiverGrantCLIUnblocksAWaivableGateFailure is the
// real-daemon, real-binary proof for `kairos waiver grant`
// (L11-policy-secrets.md's Future work: engine.GrantWaiver already
// existed and enforced every invariant, with no CLI/API route reaching
// it). Grants the waiver via the actual CLI command, not
// engine.GrantWaiver directly, so the API route and request shape are
// genuinely exercised end to end.
func TestIntegration_waiverGrantCLIUnblocksAWaivableGateFailure(t *testing.T) {
	bin := buildKairos(t)
	home := t.TempDir()
	t.Cleanup(func() { stopDaemon(t, home) })

	runOut := runKairos(t, bin, home, "-o", "json", "run", waivableGateDefPath(t))
	var created struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal([]byte(runOut), &created); err != nil {
		t.Fatalf("decoding run output %q: %v", runOut, err)
	}
	runID := created.RunID
	if runID == "" {
		t.Fatalf("no runId in run output: %q", runOut)
	}

	// Grant the waiver via the real CLI. Unlike gates_waiver_test.go's
	// in-process engine.GrantWaiver call (effectively zero latency
	// relative to the shard's own processing), this is a genuinely
	// separate OS process racing a live daemon — with
	// maxIterationsPerNode: 1, the gate is evaluated exactly once, so the
	// grant must land before that single evaluation. n1's `sleep 1`
	// exists to guarantee a real window: gate evaluation only runs after
	// NodeOutputReceived, so the grant call (fork/exec/socket-connect,
	// comfortably under a second) has the whole sleep to complete in.
	grantOut := runKairos(t, bin, home, "waiver", "grant", runID,
		"--node", "n1", "--gate", "guardrails", "--reason", "known flaky check, tracked", "--ttl", "1h")
	if strings.TrimSpace(grantOut) != "granted" {
		t.Fatalf("waiver grant output = %q, want \"granted\"", grantOut)
	}

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		showOut := runKairos(t, bin, home, "-o", "json", "show", runID)
		if strings.Contains(showOut, `"succeeded"`) {
			return
		}
		if strings.Contains(showOut, `"failed"`) {
			t.Fatalf("run failed despite a valid waiver for its only failing, waivable gate: %s", showOut)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("run did not reach succeeded within the deadline")
}

// TestIntegration_waiverGrantAndEffectsConfirmCLIValidation proves the
// real daemon enforces both verbs' required flags/values over the actual
// HTTP wire — no --yes/--all bypass exists for either, matching kairos
// approve's discipline.
func TestIntegration_waiverGrantAndEffectsConfirmCLIValidation(t *testing.T) {
	bin := buildKairos(t)
	home := t.TempDir()
	t.Cleanup(func() { stopDaemon(t, home) })

	// A cheap real run to grant/confirm against — its actual outcome
	// doesn't matter for this test, only that the run id resolves.
	runOut := runKairos(t, bin, home, "-o", "json", "run", milestoneNoAdoptPath(t))
	var created struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal([]byte(runOut), &created); err != nil {
		t.Fatalf("decoding run output %q: %v", runOut, err)
	}
	runID := created.RunID

	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"waiver grant missing --node", []string{"waiver", "grant", runID, "--gate", "g", "--reason", "r", "--ttl", "1h"}, "--node is required"},
		{"waiver grant missing --gate", []string{"waiver", "grant", runID, "--node", "n1", "--reason", "r", "--ttl", "1h"}, "--gate is required"},
		{"waiver grant missing --reason", []string{"waiver", "grant", runID, "--node", "n1", "--gate", "g", "--ttl", "1h"}, "--reason is required"},
		{"waiver grant missing --ttl", []string{"waiver", "grant", runID, "--node", "n1", "--gate", "g", "--reason", "r"}, "--ttl is required"},
		{"effects confirm missing --node", []string{"effects", "confirm", runID, "--effect", "git.push", "--scope", "once"}, "--node is required"},
		{"effects confirm missing --effect", []string{"effects", "confirm", runID, "--node", "n1", "--scope", "once"}, "--effect is required"},
		{"effects confirm bad --scope", []string{"effects", "confirm", runID, "--node", "n1", "--effect", "git.push", "--scope", "sometimes"}, "--scope must be"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd := exec.Command(bin, c.args...)
			cmd.Env = append(os.Environ(), "KAIROS_HOME="+home)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("expected rejection, got success: %s", out)
			}
			if !strings.Contains(string(out), c.wantErr) {
				t.Fatalf("error output = %q, want it to contain %q", out, c.wantErr)
			}
		})
	}
}
