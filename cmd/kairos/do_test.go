package main_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// writeFakeDoLLM writes a minimal fake CLI standing in for claude — the
// established never-call-a-real-LLM-in-automated-tests convention this
// whole project follows (see internal/engine's writeFakeLLM). It ignores
// most of the real per-CLI argv shape and just proves the file contract:
// read $KAIROS_OUTPUT/$KAIROS_SCHEMA, write valid JSON, exit 0. When
// resumeMarker is non-empty, it ALSO requires --resume <resumeMarker>
// to appear somewhere in argv — turn 2's proof that a real native resume
// was requested, not a fresh session.
func writeFakeDoLLM(t *testing.T, resumeMarker string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-claude.sh")
	script := `#!/bin/sh
set -e
`
	if resumeMarker != "" {
		script += `
prev=""
found=0
for arg in "$@"; do
  if [ "$prev" = "--resume" ] && [ "$arg" = "` + resumeMarker + `" ]; then
    found=1
  fi
  prev="$arg"
done
if [ "$found" != "1" ]; then
  echo "expected --resume ` + resumeMarker + `" >&2
  exit 1
fi
`
	}
	script += `echo '{"result":"ok from fake claude"}' > "$KAIROS_OUTPUT"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // a fixture executable, not production output
		t.Fatalf("writing fake claude: %v", err)
	}
	return path
}

func (h *daemonHarness) postDo(t *testing.T, text, continueRunID string) (runID, conversationRunID string) {
	t.Helper()
	client := h.httpClient()
	body, _ := json.Marshal(map[string]string{"text": text, "continueRunId": continueRunID})
	resp, err := client.Post("http://kairos/do", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 201 {
		t.Fatalf("POST /do: status %d", resp.StatusCode)
	}
	var out struct {
		RunID             string `json:"runId"`
		ConversationRunID string `json:"conversationRunId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding /do response: %v", err)
	}
	return out.RunID, out.ConversationRunID
}

// TestKairosDo_createsARunAndPopulatesTheConversation is POST /do's
// end-to-end proof: a real run is created through the SAME
// tasksource.CreateRun path every trigger uses, the user's own text lands
// in the Conversation immediately, and the eventual real (fake-CLI-stub)
// output lands there too as the reply — the "chat" experience `kairos
// do`/the web chat/the TUI composer all now share.
func TestKairosDo_createsARunAndPopulatesTheConversation(t *testing.T) {
	bin := buildKairos(t)
	home := t.TempDir()
	fakeCLI := writeFakeDoLLM(t, "")

	h := newDaemonHarness(t, bin, home)
	h.extraEnv = []string{"KAIROS_LLM_BINARY=" + fakeCLI}
	h.start(t, 5*time.Second)
	h.waitForReconciled(t, 3*time.Second)

	runID, conversationRunID := h.postDo(t, "say hello", "")
	if runID != conversationRunID {
		t.Fatalf("turn one: runID (%s) != conversationRunID (%s), want equal", runID, conversationRunID)
	}

	deadline := time.Now().Add(10 * time.Second)
	var status string
	for time.Now().Before(deadline) {
		status = h.runStatus(t, runID)
		if status == "succeeded" || status == "failed" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if status != "succeeded" {
		t.Fatalf("run status = %q, want succeeded", status)
	}

	client := h.httpClient()
	resp, err := client.Get("http://kairos/runs/" + conversationRunID + "/conversation")
	if err != nil {
		t.Fatalf("GET conversation: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var conv struct {
		Messages []struct{ Role, Text string }
	}
	if err := json.NewDecoder(resp.Body).Decode(&conv); err != nil {
		t.Fatalf("decoding conversation: %v", err)
	}
	var sawHuman, sawAssistant bool
	for _, m := range conv.Messages {
		if m.Role == "human" && m.Text == "say hello" {
			sawHuman = true
		}
		if m.Role == "assistant" && strings.Contains(m.Text, "ok from fake claude") {
			sawAssistant = true
		}
	}
	if !sawHuman {
		t.Errorf("conversation does not contain the user's own message: %+v", conv.Messages)
	}
	if !sawAssistant {
		t.Errorf("conversation does not contain the actor's real output as a reply: %+v", conv.Messages)
	}

	if mismatches := h.dbVerify(t); len(mismatches) != 0 {
		t.Errorf("db verify found mismatches: %v", mismatches)
	}
}

// TestKairosDo_continuationResumesTheSameSession proves turn two is a
// REAL native --resume, not a fresh, unrelated session: the fake CLI
// refuses to succeed unless invoked with --resume <the exact prior
// session id>, which internal/api/do.go's handler can only supply by
// reading turn one's real llm.session.started event.
func TestKairosDo_continuationResumesTheSameSession(t *testing.T) {
	bin := buildKairos(t)
	home := t.TempDir()

	h := newDaemonHarness(t, bin, home)
	h.extraEnv = []string{"KAIROS_LLM_BINARY=" + writeFakeDoLLM(t, "")}
	h.start(t, 5*time.Second)
	h.waitForReconciled(t, 3*time.Second)

	runID, conversationRunID := h.postDo(t, "turn one", "")
	waitForRunTerminal(t, h, runID)

	sessionID := readLastSessionID(t, h, runID)
	if sessionID == "" {
		t.Fatal("no llm.session.started event found for turn one")
	}

	// Restart the daemon (same $KAIROS_HOME, so it's the SAME event log
	// and conversation) with a fake CLI that specifically demands
	// --resume <sessionID> — proving the same id round-tripped through
	// the daemon's own continuation logic (internal/api/do.go's
	// lastAdHocSessionID), not a coincidence.
	syscall.Kill(-h.cmd.Process.Pid, syscall.SIGTERM) //nolint:errcheck // best-effort; h2 binds the same socket regardless
	h2 := newDaemonHarness(t, bin, home)
	h2.extraEnv = []string{"KAIROS_LLM_BINARY=" + writeFakeDoLLM(t, sessionID)}
	h2.start(t, 10*time.Second)
	h2.waitForReconciled(t, 5*time.Second)

	turn2RunID, turn2ConversationRunID := h2.postDo(t, "turn two", conversationRunID)
	if turn2ConversationRunID != conversationRunID {
		t.Fatalf("turn two conversationRunId = %s, want it to stay %s", turn2ConversationRunID, conversationRunID)
	}
	if turn2RunID == runID {
		t.Fatal("turn two must create a NEW run, not reuse turn one's")
	}
	waitForRunTerminal(t, h2, turn2RunID)

	status := h2.runStatus(t, turn2RunID)
	if status != "succeeded" {
		t.Fatalf("turn two status = %q, want succeeded — the fake CLI only succeeds given the exact prior session id, so this means --resume was NOT passed correctly", status)
	}
}

func waitForRunTerminal(t *testing.T, h *daemonHarness, runID string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s := h.runStatus(t, runID)
		if s == "succeeded" || s == "failed" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("run %s did not reach a terminal status in time", runID)
}

func readLastSessionID(t *testing.T, h *daemonHarness, runID string) string {
	t.Helper()
	envs := h.streamEnvelopes(t, runID)
	var sessionID string
	for _, e := range envs {
		if e.EventType != "llm.session.started" {
			continue
		}
		var payload struct {
			NodeID    string
			SessionID string
		}
		if err := json.Unmarshal(e.Event, &payload); err == nil && payload.NodeID == "task" {
			sessionID = payload.SessionID
		}
	}
	return sessionID
}
