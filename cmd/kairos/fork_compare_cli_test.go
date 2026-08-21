package main_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// forkableWorkflowPath is a simple two-node rule/shell workflow with no
// workspace: write nodes — Engine.Fork's own needsWorkspace check makes
// forking such a run require no git snapshot machinery at all, keeping
// this test real without needing a git remote fixture.
func forkableWorkflowPath(t *testing.T) string {
	t.Helper()
	const yaml = `
name: forkable
params: { greeting: "string!" }
nodes:
  - id: n1
    actor: rule
    output: { x: "string" }
  - id: n2
    actor: shell
    prompt: "echo '{\"ok\":true}' > \"$KAIROS_OUTPUT_PATH\""
    output: { ok: "bool!" }
`
	defPath := filepath.Join(t.TempDir(), "forkable.yaml")
	if err := os.WriteFile(defPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}
	return defPath
}

// TestIntegration_forkAndCompareCLI is L18-fork-replay-verify.md's own
// named Future work: "a committed cmd/kairos end-to-end test for kairos
// fork/kairos compare against the real built binary... this document's
// own verification ran the equivalent by hand." Drives both verbs
// through the real binary against a real daemon: forks a completed run,
// confirms the forked run's event log correctly starts with the copied
// prefix tagged run.forked, then confirms kairos compare reports both
// runs correctly including the fork relationship.
func TestIntegration_forkAndCompareCLI(t *testing.T) {
	bin := buildKairos(t)
	home := t.TempDir()
	t.Cleanup(func() { stopDaemon(t, home) })

	h := newDaemonHarness(t, bin, home)
	h.start(t, 5*time.Second)
	h.waitForReconciled(t, 3*time.Second)

	runOut := runKairos(t, bin, home, "-o", "json", "run", forkableWorkflowPath(t), "--param", "greeting=original-hello")
	var created struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal([]byte(runOut), &created); err != nil {
		t.Fatalf("decoding run output %q: %v", runOut, err)
	}
	origRunID := created.RunID

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		showOut := runKairos(t, bin, home, "-o", "json", "show", origRunID)
		if strings.Contains(showOut, `"succeeded"`) {
			break
		}
		if strings.Contains(showOut, `"failed"`) {
			t.Fatalf("original run failed: %s", showOut)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Fork with a param override — proves --set actually reaches the
	// engine, not just that a new run gets created.
	forkOut := runKairos(t, bin, home, "-o", "json", "fork", origRunID, "--set", "greeting=forked-hello")
	var forkResult struct {
		NewRunID string `json:"NewRunID"`
		Drifted  bool   `json:"Drifted"`
	}
	if err := json.Unmarshal([]byte(forkOut), &forkResult); err != nil {
		t.Fatalf("decoding fork output %q: %v", forkOut, err)
	}
	if forkResult.NewRunID == "" {
		t.Fatalf("fork output has no NewRunID: %q", forkOut)
	}
	if forkResult.NewRunID == origRunID {
		t.Fatal("fork returned the SAME run id as the original — not a real fork")
	}

	// Confirm the forked run's event log correctly starts with the
	// copied prefix, tagged run.forked with the right FromRunID — the
	// core claim 06-durability.md makes about what fork actually does.
	envs := h.streamEnvelopes(t, forkResult.NewRunID)
	if len(envs) == 0 {
		t.Fatal("forked run has no events at all")
	}
	if envs[0].EventType != "trigger.received" {
		t.Fatalf("forked run's first event = %q, want trigger.received (the copied prefix)", envs[0].EventType)
	}
	// --set's override must land in the copied trigger.received's own
	// Params — the concrete, verifiable proof overrides actually reach
	// the new run (proving the override changed the actor's own runtime
	// behavior would need per-node input-binding this codebase doesn't
	// have yet — NL-37 — so this checks what Fork itself guarantees).
	var triggerPayload struct {
		Params json.RawMessage
	}
	if err := json.Unmarshal(envs[0].Event, &triggerPayload); err != nil {
		t.Fatalf("decoding trigger.received payload: %v", err)
	}
	if !strings.Contains(string(triggerPayload.Params), "forked-hello") {
		t.Fatalf("forked run's trigger.received Params = %s, want the --set override reflected", triggerPayload.Params)
	}
	var sawForked bool
	for _, e := range envs {
		if e.EventType == "run.forked" {
			sawForked = true
			var payload struct{ FromRunID string }
			if err := json.Unmarshal(e.Event, &payload); err != nil {
				t.Fatalf("decoding run.forked payload: %v", err)
			}
			if payload.FromRunID != origRunID {
				t.Fatalf("run.forked FromRunID = %q, want %q", payload.FromRunID, origRunID)
			}
		}
	}
	if !sawForked {
		t.Fatal("forked run's event log has no run.forked event")
	}

	// The forked run must independently reach a terminal state too — a
	// fork that never finishes proves nothing about replay working.
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		showOut := runKairos(t, bin, home, "-o", "json", "show", forkResult.NewRunID)
		if strings.Contains(showOut, `"succeeded"`) {
			break
		}
		if strings.Contains(showOut, `"failed"`) {
			t.Fatalf("forked run failed: %s", showOut)
		}
		time.Sleep(100 * time.Millisecond)
	}

	compareOut := runKairos(t, bin, home, "-o", "json", "compare", origRunID, forkResult.NewRunID)
	var compareResult struct {
		A struct {
			RunID  string
			Status string
		}
		B struct {
			RunID      string
			Status     string
			ForkedFrom string
		}
	}
	if err := json.Unmarshal([]byte(compareOut), &compareResult); err != nil {
		t.Fatalf("decoding compare output %q: %v", compareOut, err)
	}
	if compareResult.A.RunID != origRunID || compareResult.B.RunID != forkResult.NewRunID {
		t.Fatalf("compare result = %+v, want A=%s B=%s", compareResult, origRunID, forkResult.NewRunID)
	}
	if compareResult.A.Status != "succeeded" || compareResult.B.Status != "succeeded" {
		t.Fatalf("compare statuses = %q, %q, want both succeeded", compareResult.A.Status, compareResult.B.Status)
	}
	if compareResult.B.ForkedFrom != origRunID {
		t.Fatalf("compare's B.ForkedFrom = %q, want %q — the fork relationship must be visible in kairos compare", compareResult.B.ForkedFrom, origRunID)
	}

	if mismatches := h.dbVerify(t); len(mismatches) != 0 {
		t.Errorf("db verify found mismatches after fork+compare: %v", mismatches)
	}
}
