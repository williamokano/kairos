package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/williamokano/kairos/internal/cli"
	"github.com/williamokano/kairos/internal/web"
)

// TestPostMessage_doCreatedRunContinuesViaDo is a regression test for a
// real bug found via live testing: posting a message to a `kairos
// do`-created run's Conversation ("stores in the database but I don't
// see any output") was a dumb append with nothing to react to it. This
// run's own conversation IS a session's ConversationRunID, so the fix
// must route through DoWithSession with the SESSION id, not a bare
// continueRunId — proving the run/session lookup, not just the
// do-detection, is wired correctly.
func TestPostMessage_doCreatedRunContinuesViaSession(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	fd.events = []cli.Envelope{
		{StreamID: "run_1", Sequence: 1, EventType: "trigger.received", Event: []byte(`{"TriggerRef":"do:01ABC"}`)},
	}
	fd.sessions = map[string]cli.Session{
		"ses_1": {ID: "ses_1", ConversationRunID: "run_1"},
	}
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodPost, "/c/run_1/messages", deps.Token, "text=continue+please"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if len(fd.doCalls) != 1 {
		t.Fatalf("expected exactly one POST /do call, got %d: %+v", len(fd.doCalls), fd.doCalls)
	}
	call := fd.doCalls[0]
	if call.SessionID != "ses_1" {
		t.Errorf("SessionID = %q, want %q (the run's owning session)", call.SessionID, "ses_1")
	}
	if call.Text != "continue please" {
		t.Errorf("Text = %q, want the posted message", call.Text)
	}
}

// TestPostMessage_doCreatedRunWithNoSessionContinuesByRun proves the
// plain (session-less) ad hoc chat case still works: continuation by run
// id when no Session owns this run's conversation.
func TestPostMessage_doCreatedRunWithNoSessionContinuesByRun(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	fd.events = []cli.Envelope{
		{StreamID: "run_2", Sequence: 1, EventType: "trigger.received", Event: []byte(`{"TriggerRef":"do:01XYZ"}`)},
	}
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodPost, "/c/run_2/messages", deps.Token, "text=hi+again"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if len(fd.doCalls) != 1 {
		t.Fatalf("expected exactly one POST /do call, got %d", len(fd.doCalls))
	}
	if fd.doCalls[0].ContinueRunID != "run_2" {
		t.Errorf("ContinueRunID = %q, want %q", fd.doCalls[0].ContinueRunID, "run_2")
	}
	if fd.doCalls[0].SessionID != "" {
		t.Errorf("SessionID = %q, want empty (no owning session)", fd.doCalls[0].SessionID)
	}
}

// TestPostMessage_handAuthoredWorkflowStillUsesPlainAppend is the
// no-regression proof: a run NOT created by `kairos do` (a real
// hand-authored workflow's wait: conversation node) must keep using the
// existing, correct dumb-append path — the engine's own live subscription
// resolves the wait, and routing this through /do would be a genuine
// behavior change to a case that already works.
func TestPostMessage_handAuthoredWorkflowStillUsesPlainAppend(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	fd.events = []cli.Envelope{
		{StreamID: "run_3", Sequence: 1, EventType: "trigger.received", Event: []byte(`{"TriggerRef":"cli:kairos-run"}`)},
	}
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodPost, "/c/run_3/messages", deps.Token, "text=approve"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if len(fd.doCalls) != 0 {
		t.Errorf("expected NO POST /do call for a hand-authored workflow's run, got %d", len(fd.doCalls))
	}
}

// TestSessionChatPage_rendersFullThreadAndInputBox proves the
// session-centric chat page shows a session's entire conversation
// history (not just its most recent turn) with a persistent input box
// whose form carries the session id from the URL path, not a droppable
// form field — the exact bug class the two-step /chat picker had.
func TestSessionChatPage_rendersFullThreadAndInputBox(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	fd.sessions = map[string]cli.Session{
		"ses_1": {ID: "ses_1", Actor: "claude", WorkDir: "/some/dir", ConversationRunID: "run_1"},
	}
	fd.messages = []cli.ConversationMessage{
		{Role: "human", Text: "turn one"},
		{Role: "assistant", Text: "reply one"},
		{Role: "human", Text: "turn two"},
		{Role: "assistant", Text: "reply two"},
	}
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodGet, "/sessions/ses_1", deps.Token, ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"turn one", "reply one", "turn two", "reply two"} {
		if !strings.Contains(body, want) {
			t.Errorf("session page missing message %q in rendered thread:\n%s", want, body)
		}
	}
	if !strings.Contains(body, `hx-post="/sessions/ses_1/messages"`) {
		t.Error("session page's input form does not post to this session's own fixed path — sessionId could be lost, same bug class as the two-step /chat picker")
	}
}

// TestSessionChatSend_alwaysThreadsSessionIDFromPath proves the fix's
// core safety property: the session id used to continue the chat comes
// from the URL PATH, not any form field a submission could omit.
func TestSessionChatSend_alwaysThreadsSessionIDFromPath(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	fd.sessions = map[string]cli.Session{"ses_9": {ID: "ses_9"}}
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodPost, "/sessions/ses_9/messages", deps.Token, "text=hello"))

	if rec.Code != http.StatusSeeOther && rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if len(fd.doCalls) != 1 {
		t.Fatalf("expected exactly one POST /do call, got %d", len(fd.doCalls))
	}
	if fd.doCalls[0].SessionID != "ses_9" {
		t.Errorf("SessionID = %q, want %q (from the URL path, not a form field)", fd.doCalls[0].SessionID, "ses_9")
	}
}

// TestSessionChatSend_sessionIDNeverLostAcrossSequentialTurns is a
// regression test for a real failure a user hit live: a follow-up
// message sent from a page state reached via a redirect carried NO
// sessionId at all — the synthesized ad hoc definition had no
// workDirOverride, exactly as if no session had ever been picked. That
// happened because the old /chat flow threaded sessionId through a
// hidden FORM FIELD that had to survive a redirect correctly; if
// anything reset or repopulated that field wrong, the value silently
// vanished with no error anywhere. The /sessions/{id} page's fix is
// architectural, not defensive: the session id lives in the URL PATH,
// which every request to this handler reads directly — there is no
// carried value to lose. This test drives the exact sequence the user
// hit — load the page, send a message, load the page again (simulating
// the post-redirect page state), send a second message — and asserts
// BOTH turns carried the correct, non-empty session id.
func TestSessionChatSend_sessionIDNeverLostAcrossSequentialTurns(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	fd.sessions = map[string]cli.Session{"ses_multi": {ID: "ses_multi", ConversationRunID: "run_multi"}}
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	get := func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, authedRequest(http.MethodGet, "/sessions/ses_multi", deps.Token, ""))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /sessions/ses_multi: status = %d, body: %s", rec.Code, rec.Body.String())
		}
	}
	send := func(text string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, authedRequest(http.MethodPost, "/sessions/ses_multi/messages", deps.Token, "text="+text))
		if rec.Code != http.StatusSeeOther && rec.Code != http.StatusOK {
			t.Fatalf("POST /sessions/ses_multi/messages(%q): status = %d, body: %s", text, rec.Code, rec.Body.String())
		}
	}

	get()             // turn 1's page load
	send("first+msg") // turn 1's send
	get()             // the post-redirect page state turn 2 is sent from — NOT a fresh top-level navigation with fresh query params
	send("second+msg")

	if len(fd.doCalls) != 2 {
		t.Fatalf("expected exactly two POST /do calls, got %d: %+v", len(fd.doCalls), fd.doCalls)
	}
	for i, call := range fd.doCalls {
		if call.SessionID != "ses_multi" {
			t.Errorf("turn %d: SessionID = %q, want %q — session id was lost across sequential turns, the exact bug this page exists to prevent", i+1, call.SessionID, "ses_multi")
		}
	}
}
