package main_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// slowCancelableWorkflowPath is a one-node shell workflow that sleeps long
// enough for a real `kairos cancel` invocation to reliably land while the
// node is genuinely Executing.
func slowCancelableWorkflowPath(t *testing.T) string {
	t.Helper()
	const yaml = `
name: cancelable
nodes:
  - id: n1
    actor: shell
    prompt: |
      sleep 3
      echo '{"ok":true}' > "$KAIROS_OUTPUT_PATH"
    output: { ok: "bool!" }
`
	defPath := filepath.Join(t.TempDir(), "cancelable.yaml")
	if err := os.WriteFile(defPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}
	return defPath
}

// TestIntegration_cancelCLI is L23-webui-revamp.md's own real end-to-end
// proof for `kairos cancel` — a verb (and its daemon route,
// engine.Engine.Cancel) that did not exist anywhere in this tree before
// this pass. Drives the real built binary against a real daemon: starts a
// genuinely long-running node, cancels it mid-flight, confirms the run
// reaches "cancelled" (not merely that the CLI returned 0), confirms the
// node's own execution recorded node.execution.interrupted, and closes
// with a real `db verify` — the same proof shape
// TestIntegration_forkAndCompareCLI already establishes for fork/compare.
func TestIntegration_cancelCLI(t *testing.T) {
	bin := buildKairos(t)
	home := t.TempDir()
	t.Cleanup(func() { stopDaemon(t, home) })

	h := newDaemonHarness(t, bin, home)
	h.start(t, 5*time.Second)
	h.waitForReconciled(t, 3*time.Second)

	runOut := runKairos(t, bin, home, "run", slowCancelableWorkflowPath(t))
	fields := strings.Fields(runOut)
	if len(fields) == 0 {
		t.Fatalf("kairos run produced no run id: %q", runOut)
	}
	runID := fields[0]

	// Wait for the node to genuinely be executing before cancelling — a
	// cancel raced against dispatch itself is a different, untested shape.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		showOut := runKairos(t, bin, home, "-o", "json", "show", runID)
		if strings.Contains(showOut, `"executing"`) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	cancelOut := runKairos(t, bin, home, "cancel", runID, "--reason", "no longer needed")
	if !strings.Contains(cancelOut, "cancelled") {
		t.Fatalf("kairos cancel output = %q, want to contain \"cancelled\"", cancelOut)
	}

	deadline = time.Now().Add(10 * time.Second)
	var finalOut string
	for time.Now().Before(deadline) {
		finalOut = runKairos(t, bin, home, "-o", "json", "show", runID)
		if strings.Contains(finalOut, `"cancelled"`) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(finalOut, `"cancelled"`) {
		t.Fatalf("run never reached cancelled: %s", finalOut)
	}

	envs := h.streamEnvelopes(t, runID)
	var sawCancelled, sawInterrupted bool
	for _, e := range envs {
		switch e.EventType {
		case "run.cancelled":
			sawCancelled = true
		case "node.execution.interrupted":
			sawInterrupted = true
		}
	}
	if !sawCancelled {
		t.Error("run's event log has no run.cancelled event")
	}
	if !sawInterrupted {
		t.Error("run's event log has no node.execution.interrupted event — the node the run cancelled was in flight and must be recorded as interrupted")
	}

	// A second cancel attempt must be rejected — a terminal run cannot be
	// cancelled twice.
	secondOut, err := runKairosExpectingError(t, bin, home, "cancel", runID, "--reason", "again")
	if err == nil {
		t.Errorf("a second cancel on an already-cancelled run must be rejected, got exit 0 with output: %q", secondOut)
	}

	if mismatches := h.dbVerify(t); len(mismatches) != 0 {
		t.Errorf("db verify found mismatches after cancel: %v", mismatches)
	}
}
