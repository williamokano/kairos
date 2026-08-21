package main_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// humanApprovalDefPath is the same shape verified in CLI-GUIDE.md's own
// demo: a shell node producing a summary, then a wait: human node.
func humanApprovalDefPath(t *testing.T) string {
	t.Helper()
	const yaml = `
name: demo-approval
nodes:
  - id: propose
    actor: shell
    prompt: echo '{"summary":"delete the staging bucket"}' > "$KAIROS_OUTPUT_PATH"
    output: { summary: "string!" }
  - id: approve
    actor: human
    workspace: none
    inputs:
      summary: "$.outputs.propose.summary"
    output: { decision: "string!", reason: "string" }
    wait:
      "on":
        - kind: human
      weight: read
      timeout: 1h
      onTimeout: park
`
	defPath := filepath.Join(t.TempDir(), "demo-approval.yaml")
	if err := os.WriteFile(defPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}
	return defPath
}

// TestIntegration_humanTasksCLIReflectsARealParkedTaskThenClosesIt is the
// real-daemon proof for L20-webui.md's Documented decision #5: `kairos
// human-tasks` (backed by GET /human-tasks, HumanTaskIndexProjection's
// real index) must show a genuinely parked wait: human node, and must
// stop showing it once `kairos approve` answers it — proving the index
// tracks real opens AND real closes, not just accumulating rows.
func TestIntegration_humanTasksCLIReflectsARealParkedTaskThenClosesIt(t *testing.T) {
	bin := buildKairos(t)
	home := t.TempDir()
	t.Cleanup(func() { stopDaemon(t, home) })

	runOut := runKairos(t, bin, home, "-o", "json", "run", humanApprovalDefPath(t))
	var created struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal([]byte(runOut), &created); err != nil {
		t.Fatalf("decoding run output %q: %v", runOut, err)
	}
	runID := created.RunID

	deadline := time.Now().Add(10 * time.Second)
	var sawWaiting bool
	for time.Now().Before(deadline) {
		tasksOut := runKairos(t, bin, home, "-o", "json", "human-tasks")
		if strings.Contains(tasksOut, runID) && strings.Contains(tasksOut, "approve") {
			sawWaiting = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !sawWaiting {
		t.Fatal("kairos human-tasks never showed the parked approve node")
	}

	approveOut := runKairos(t, bin, home, "approve", runID,
		"--node", "approve", "--confirm", "approve", "--reason", "reviewed, staging is empty")
	if strings.TrimSpace(approveOut) != "answered" {
		t.Fatalf("approve output = %q, want \"answered\"", approveOut)
	}

	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		tasksOut := runKairos(t, bin, home, "-o", "json", "human-tasks")
		if !strings.Contains(tasksOut, runID) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("kairos human-tasks still shows the run after it was answered — the index row was never closed")
}
