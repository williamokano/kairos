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

// TestIntegration_effectsListAndResolveCLI is the real-daemon proof for
// L12-effects-compensation.md's Future work: "kairos run effects <run>"
// and the EffectUnknown resolution CLI verb, previously daemon-side data/
// logic with no CLI verb reaching them at all (internal/engine's own
// TestEngine_effectsListsRecordedActionsAndResolveUnblocksAnUnknown/
// TestEngine_resolveEffectUnknownAppliedUnblocksTheRun already prove the
// underlying state-machine logic in isolation, including the
// mess-exists-before-resolving discipline; this test proves the CLI
// binary's flag plumbing and the daemon's HTTP wiring for both verbs
// actually work end to end).
func TestIntegration_effectsListAndResolveCLI(t *testing.T) {
	bin := buildKairos(t)
	home := t.TempDir()
	t.Cleanup(func() { stopDaemon(t, home) })

	// A run with no effects at all: `kairos effects` must return a
	// genuinely empty list from the real daemon, not an error — the
	// simplest possible real proof the GET route/CLI verb round-trips
	// correctly.
	runOut := runKairos(t, bin, home, "-o", "json", "run", milestoneNoAdoptPath(t))
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

	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		showOut := runKairos(t, bin, home, "-o", "json", "show", runID)
		if strings.Contains(showOut, `"succeeded"`) || strings.Contains(showOut, `"failed"`) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	effectsOut := runKairos(t, bin, home, "-o", "json", "effects", runID)
	if strings.TrimSpace(effectsOut) != "[]" && strings.TrimSpace(effectsOut) != "null" {
		t.Fatalf("effects for a run with no effect nodes = %q, want an empty list", effectsOut)
	}

	// The resolve verb's real, over-the-wire validation — proving the
	// daemon actually enforces "never a rubber stamp" rather than the
	// CLI merely choosing not to offer a bypass flag: a well-formed
	// request against a run with nothing to resolve gets a real,
	// non-crashing 422 from the daemon, and malformed flag combinations
	// are rejected by the CLI itself before any request is even sent.
	cmd := exec.Command(bin, "effects", "resolve", runID, "--node", "n1", "--outcome", "sideways", "--reason", "x")
	cmd.Env = append(os.Environ(), "KAIROS_HOME="+home)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected --outcome sideways to be rejected, got success: %s", out)
	}
	if !strings.Contains(string(out), "outcome must be") {
		t.Fatalf("error output = %q, want a message naming the invalid --outcome", out)
	}

	cmd = exec.Command(bin, "effects", "resolve", runID, "--node", "n1", "--outcome", "applied")
	cmd.Env = append(os.Environ(), "KAIROS_HOME="+home)
	out, err = cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected a missing --reason to be rejected, got success: %s", out)
	}
	if !strings.Contains(string(out), "--reason is required") {
		t.Fatalf("error output = %q, want a message naming the missing --reason", out)
	}

	cmd = exec.Command(bin, "effects", "resolve", runID, "--node", "n1", "--outcome", "applied", "--reason", "no effect node here")
	cmd.Env = append(os.Environ(), "KAIROS_HOME="+home)
	out, err = cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected resolving a node with no Executing effect exec to be rejected by the daemon, got success: %s", out)
	}
	if !strings.Contains(string(out), "Executing execution") {
		t.Fatalf("error output = %q, want the daemon's real invariant-violation message", out)
	}
}

// milestoneNoAdoptPath is a trivial, effect-free two-node workflow — this
// test only needs a run that reaches a terminal state quickly with zero
// effect actions.
func milestoneNoAdoptPath(t *testing.T) string {
	t.Helper()
	defPath := filepath.Join(t.TempDir(), "no-effects.yaml")
	yaml := `
name: no-effects
nodes:
  - id: n1
    actor: rule
    output: { x: "string" }
`
	if err := os.WriteFile(defPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}
	return defPath
}
