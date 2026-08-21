package tui

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/williamokano/kairos/internal/cli"
)

func writeFakeDoLLMForTUI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-claude.sh")
	script := "#!/bin/sh\necho '{\"result\":\"hi from the TUI test\"}' > \"$KAIROS_OUTPUT\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // fixture executable
		t.Fatalf("writing fake claude: %v", err)
	}
	return path
}

// TestSubmitComposer_callsRealDoEndpointAndNavigatesToConversation is the
// regression test for L15-tui.md's own Future work stub: submitComposer
// used to just set a status line and never call anything. This drives
// the real key handling (typing into the Home composer, pressing Enter),
// runs the resulting tea.Cmd against a REAL daemon (fake LLM CLI, never a
// real network call — this project's established convention), and
// confirms the model lands on ScreenConversation with isAdHoc set and the
// real run id wired in.
func TestSubmitComposer_callsRealDoEndpointAndNavigatesToConversation(t *testing.T) {
	bin := buildKairosForSSETest(t)
	home := t.TempDir()
	fakeCLI := writeFakeDoLLMForTUI(t)
	sockPath, cleanup := startRealDaemon(t, bin, home, "KAIROS_LLM_BINARY="+fakeCLI)
	defer cleanup()

	client := cli.NewClient(sockPath)
	m := New(context.Background(), client, home, true)
	m.screen = ScreenHome
	m.mode = ModeINPUT
	m.home.compose = "say hello"

	newModel, cmd := m.submitComposer()
	m = newModel.(Model)
	if m.home.compose != "" {
		t.Errorf("home.compose = %q, want cleared after submit", m.home.compose)
	}
	if cmd == nil {
		t.Fatal("submitComposer returned a nil tea.Cmd — nothing will ever call POST /do")
	}

	msgCh := make(chan tea.Msg, 1)
	go func() { msgCh <- cmd() }()

	var msg tea.Msg
	select {
	case msg = <-msgCh:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the do command to complete")
	}

	dm, ok := msg.(doResultMsg)
	if !ok {
		t.Fatalf("cmd() produced %T, want doResultMsg", msg)
	}
	if dm.err != nil {
		t.Fatalf("kairos do failed: %v", dm.err)
	}
	if dm.resp.RunID == "" {
		t.Fatal("doResultMsg has no RunID — no real run was created")
	}

	updated, _ := m.Update(dm)
	m = updated.(Model)
	if m.screen != ScreenConversation {
		t.Errorf("screen = %v, want ScreenConversation", m.screen)
	}
	if m.conversation.runID != dm.resp.ConversationRunID {
		t.Errorf("conversation.runID = %q, want %q", m.conversation.runID, dm.resp.ConversationRunID)
	}
	if !m.conversation.isAdHoc {
		t.Error("conversation.isAdHoc = false, want true — a kairos-do conversation must use Do(continueRunId) to send a follow-up, not a plain message append")
	}

	// Real confirmation the run actually happened, not just that the
	// message pipeline was wired: poll the real daemon for its terminal
	// status.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		state, err := client.GetRun(ctx, dm.resp.RunID)
		if err == nil && (state.Status == "succeeded" || state.Status == "failed") {
			if state.Status != "succeeded" {
				t.Fatalf("run status = %s, want succeeded", state.Status)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("run did not reach a terminal status in time")
		case <-time.After(100 * time.Millisecond):
		}
	}
}
