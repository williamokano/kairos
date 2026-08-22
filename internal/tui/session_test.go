package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/williamokano/kairos/internal/cli"
)

// runCmd runs a bubbletea Cmd synchronously against a deadline — every
// new-screen test below drives real network calls the same way do_test.go
// already established for kairos do.
func runCmd(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("nil tea.Cmd — nothing would ever happen")
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for command to complete")
		return nil
	}
}

// TestProjectsScreen_listsRealProjects proves the Projects screen renders
// real daemon data, not a stub — a real project is created directly via
// the client (mirroring how the web UI's own create flow ends up calling
// the same CreateProject), then fetched and rendered.
func TestProjectsScreen_listsRealProjects(t *testing.T) {
	bin := buildKairosForSSETest(t)
	home := t.TempDir()
	sockPath, cleanup := startRealDaemon(t, bin, home)
	defer cleanup()

	client := cli.NewClient(sockPath)
	ctx := context.Background()
	repoDir := t.TempDir()
	if _, err := client.CreateProject(ctx, "demo", repoDir); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	m := New(context.Background(), client, home, true)
	msg := runCmd(t, m.fetchProjects())
	pm, ok := msg.(projectsFetchedMsg)
	if !ok {
		t.Fatalf("fetchProjects produced %T, want projectsFetchedMsg", msg)
	}
	updated, _ := m.Update(pm)
	m = updated.(Model)

	if len(m.projects.projects) != 1 || m.projects.projects[0].Name != "demo" {
		t.Fatalf("projects = %+v, want exactly one project named demo", m.projects.projects)
	}
	view := m.viewProjects()
	if !strings.Contains(view, "demo") || !strings.Contains(view, repoDir) {
		t.Errorf("viewProjects() = %q, want it to show the real project's name and path", view)
	}
}

// TestSessionsScreen_startsARealSession proves starting a session from
// the TUI's own state (project + actor typed into the two-field flow)
// calls the real daemon and the resulting session is listed afterward.
func TestSessionsScreen_startsARealSession(t *testing.T) {
	bin := buildKairosForSSETest(t)
	home := t.TempDir()
	sockPath, cleanup := startRealDaemon(t, bin, home)
	defer cleanup()

	client := cli.NewClient(sockPath)
	ctx := context.Background()
	repoDir := t.TempDir()
	if _, err := client.CreateProject(ctx, "demo", repoDir); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	m := New(context.Background(), client, home, true)
	m.sessions.startProject = "demo"
	m.sessions.startActor = "claude"

	msg := runCmd(t, m.startSession(m.sessions.startProject, m.sessions.startActor))
	sm, ok := msg.(sessionStartedMsg)
	if !ok {
		t.Fatalf("startSession produced %T, want sessionStartedMsg", msg)
	}
	if sm.err != nil {
		t.Fatalf("StartSession: %v", sm.err)
	}
	if sm.session.ID == "" {
		t.Fatal("started session has no ID")
	}

	listMsg := runCmd(t, m.fetchSessions())
	lm := listMsg.(sessionsFetchedMsg)
	if lm.err != nil {
		t.Fatalf("ListSessions: %v", lm.err)
	}
	found := false
	for _, s := range lm.sessions {
		if s.ID == sm.session.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("started session %s not found in ListSessions result", sm.session.ID)
	}
}

// TestSessionChat_sessionIDNeverLostAcrossTwoMessagesInTheSameScreen is
// the exact regression class the coordinator asked for: the web UI's own
// two-step /chat?session= picker could silently drop the session id
// between messages (a real bug a live user hit — run
// 01M0K4NPC5AN5HDRRE8BV1R3BF had no workDirOverride at all). The TUI's
// architecture is supposed to make this structurally impossible — the
// session id lives in sessionChatState for the screen's whole lifetime,
// set once by navigateToSessionChat, and every send reads it back from
// there. This proves it: two full send-message round trips through the
// SAME screen instance, confirming the session's real run count advances
// by exactly one turn per message (i.e. BOTH messages actually reached
// the session — a dropped session id would silently fall back to a
// plain ad hoc run that never touches this session's run count at all).
func TestSessionChat_sessionIDNeverLostAcrossTwoMessagesInTheSameScreen(t *testing.T) {
	bin := buildKairosForSSETest(t)
	home := t.TempDir()
	fakeCLI := writeFakeDoLLMForTUI(t)
	sockPath, cleanup := startRealDaemon(t, bin, home, "KAIROS_LLM_BINARY="+fakeCLI)
	defer cleanup()

	client := cli.NewClient(sockPath)
	ctx := context.Background()
	repoDir := t.TempDir()
	if _, err := client.CreateProject(ctx, "demo", repoDir); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	session, err := client.StartSession(ctx, "demo", "claude")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	m := New(context.Background(), client, home, true)
	newModel, cmd := m.navigateToSessionChat(session.ID)
	m = newModel.(Model)
	if m.screen != ScreenSessionChat {
		t.Fatalf("screen = %v, want ScreenSessionChat", m.screen)
	}
	if m.sessionChat.sessionID != session.ID {
		t.Fatalf("sessionChat.sessionID = %q, want %q", m.sessionChat.sessionID, session.ID)
	}
	if csm, ok := runCmd(t, cmd).(sessionChatFetchedMsg); ok {
		updated, _ := m.Update(csm)
		m = updated.(Model)
	}

	sendAndWait := func(text string) {
		t.Helper()
		if m.sessionChat.sessionID != session.ID {
			t.Fatalf("sessionChat.sessionID drifted to %q before send, want %q", m.sessionChat.sessionID, session.ID)
		}
		msg := runCmd(t, m.sendSessionMessage(m.sessionChat.sessionID, text))
		dm, ok := msg.(sessionDoResultMsg)
		if !ok {
			t.Fatalf("sendSessionMessage produced %T, want sessionDoResultMsg", msg)
		}
		if dm.err != nil {
			t.Fatalf("send %q: %v", text, dm.err)
		}
		// Poll the real run to a terminal state before sending the next
		// message — the whole point is proving sequential turns in the
		// SAME screen instance never lose the id, so each turn must
		// actually finish first.
		pollCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for {
			state, err := client.GetRun(pollCtx, dm.resp.RunID)
			if err == nil && (state.Status == "succeeded" || state.Status == "failed") {
				if state.Status != "succeeded" {
					t.Fatalf("run %s status = %s, want succeeded", dm.resp.RunID, state.Status)
				}
				break
			}
			select {
			case <-pollCtx.Done():
				t.Fatalf("run %s did not finish in time", dm.resp.RunID)
			case <-time.After(100 * time.Millisecond):
			}
		}
	}

	sendAndWait("first message")
	if m.sessionChat.sessionID != session.ID {
		t.Fatalf("sessionChat.sessionID = %q after message 1, want unchanged %q", m.sessionChat.sessionID, session.ID)
	}
	sendAndWait("second message")
	if m.sessionChat.sessionID != session.ID {
		t.Fatalf("sessionChat.sessionID = %q after message 2, want unchanged %q", m.sessionChat.sessionID, session.ID)
	}

	final, err := client.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if final.RunCount != 2 {
		t.Errorf("session.RunCount = %d, want 2 — both messages must have reached THIS session, not silently fallen back to an unrelated ad hoc run", final.RunCount)
	}
}

// TestSessionEnd_requiresTheFullTypedConfirmationSequence proves the
// destructive daemon call is genuinely unreachable without both steps
// (a real reason, then the session id typed out exactly) — an attempted
// end with an empty/partial sequence must never call EndSession at all.
func TestSessionEnd_requiresTheFullTypedConfirmationSequence(t *testing.T) {
	bin := buildKairosForSSETest(t)
	home := t.TempDir()
	sockPath, cleanup := startRealDaemon(t, bin, home)
	defer cleanup()

	client := cli.NewClient(sockPath)
	ctx := context.Background()
	repoDir := t.TempDir()
	if _, err := client.CreateProject(ctx, "demo", repoDir); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	session, err := client.StartSession(ctx, "demo", "claude")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	m := New(context.Background(), client, home, true)
	newModel, _ := m.navigateToSessionChat(session.ID)
	m = newModel.(Model)

	// Press 'x' — enters the confirm sub-flow, must NOT call the daemon.
	updated, _ := m.handleSessionChatKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = updated.(Model)
	if !m.sessionChat.ending {
		t.Fatal("pressing x did not enter the end-session confirm flow")
	}

	// Pressing enter with an empty reason must not advance past step 0.
	updated, cmd := m.handleSessionEndKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd != nil {
		t.Fatal("enter with an empty reason produced a command — the daemon must never be called yet")
	}
	if m.sessionChat.endStep != 0 {
		t.Fatal("empty reason advanced past step 0")
	}

	// A real reason advances to the confirm step — this is a state
	// transition, not a daemon call, so cmd is correctly nil here too;
	// what matters is endStep actually moved to 1.
	m.sessionChat.endReason = "testing"
	updated, cmd = m.handleSessionEndKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd != nil {
		t.Fatal("advancing to the confirm step must not itself call the daemon")
	}
	if m.sessionChat.endStep != 1 {
		t.Fatal("a real reason did not advance to the confirm step")
	}
	if _, err := client.GetSession(ctx, session.ID); err != nil {
		t.Fatalf("session was affected before any confirm was typed: %v", err)
	}

	updated, cmd = m.handleSessionEndKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd != nil {
		t.Fatal("an empty confirm (not equal to the session id) produced a command — the daemon must never be called")
	}
	if _, err := client.GetSession(ctx, session.ID); err != nil {
		t.Fatalf("session was affected by an empty confirm: %v", err)
	}

	// NOW the real, full sequence: type the session id exactly, submit.
	m.sessionChat.endConfirm = session.ID
	updated, cmd = m.handleSessionEndKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	msg := runCmd(t, cmd)
	em, ok := msg.(sessionEndedMsg)
	if !ok {
		t.Fatalf("the correct typed confirmation produced %T, want sessionEndedMsg", msg)
	}
	if em.err != nil {
		t.Fatalf("EndSession with the correct confirmation failed: %v", em.err)
	}
	if _, err := client.GetSession(ctx, session.ID); err == nil {
		t.Error("session still exists after a correctly-confirmed end")
	}
}
