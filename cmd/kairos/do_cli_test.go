package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestKairosDoCLI_createsARunAndConversation exercises `kairos do` as an
// actual CLI verb (not the raw daemon socket, which the earlier tests in
// this file already cover) — proving the verb this whole feature is
// named after really works end to end, auto-starting its own daemon the
// same way any other `kairos` invocation would.
func TestKairosDoCLI_createsARunAndConversation(t *testing.T) {
	bin := buildKairos(t)
	home := t.TempDir()
	fakeCLI := writeFakeDoLLM(t, "")
	t.Cleanup(func() { stopDaemon(t, home) })

	cmd := exec.Command(bin, "-o", "json", "do", "say hello from the CLI")
	cmd.Env = append(os.Environ(), "KAIROS_HOME="+home, "KAIROS_LLM_BINARY="+fakeCLI)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("kairos do: %v\n%s", err, out)
	}
	var resp struct {
		RunID             string `json:"runId"`
		ConversationRunID string `json:"conversationRunId"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decoding `kairos do` json output %q: %v", out, err)
	}
	if resp.RunID == "" || resp.RunID != resp.ConversationRunID {
		t.Fatalf("kairos do output = %+v, want a fresh run whose runId == conversationRunId", resp)
	}

	deadline := time.Now().Add(10 * time.Second)
	var showOut string
	for time.Now().Before(deadline) {
		showCmd := exec.Command(bin, "-o", "json", "show", resp.RunID)
		showCmd.Env = append(os.Environ(), "KAIROS_HOME="+home)
		b, err := showCmd.CombinedOutput()
		if err != nil {
			t.Fatalf("kairos show: %v\n%s", err, b)
		}
		showOut = string(b)
		if strings.Contains(showOut, `"succeeded"`) || strings.Contains(showOut, `"failed"`) {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if !strings.Contains(showOut, `"succeeded"`) {
		t.Fatalf("run did not succeed: %s", showOut)
	}

	convCmd := exec.Command(bin, "-o", "json", "conversation", "show", resp.RunID)
	convCmd.Env = append(os.Environ(), "KAIROS_HOME="+home)
	convOut, err := convCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("kairos conversation show: %v\n%s", err, convOut)
	}
	if !strings.Contains(string(convOut), "say hello from the CLI") {
		t.Errorf("conversation does not contain the user's own message: %s", convOut)
	}
	if !strings.Contains(string(convOut), "ok from fake claude") {
		t.Errorf("conversation does not contain the actor's reply: %s", convOut)
	}
}
