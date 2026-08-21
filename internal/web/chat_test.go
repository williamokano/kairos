package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/williamokano/kairos/internal/cli"
	"github.com/williamokano/kairos/internal/web"
)

func TestChatPage_bareComposerWithNoRun(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodGet, "/chat", deps.Token, ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `name="text"`) {
		t.Error("chat page has no composer input")
	}
}

func TestChatPage_withRunShowsConversation(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	fd.messages = []cli.ConversationMessage{
		{Role: "human", Text: "say hello"},
		{Role: "assistant", Text: "hello yourself"},
	}
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodGet, "/chat?run=run_1", deps.Token, ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "say hello") || !strings.Contains(body, "hello yourself") {
		t.Errorf("chat page does not render the existing conversation:\n%s", body)
	}
}

func TestChatSend_callsDoAndRedirectsWithHXRedirect(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	fd.doResult = cli.DoResponse{RunID: "run_2", ConversationRunID: "run_2"}
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	body := "text=" + "do%20something" + "&continueRunId="
	h.ServeHTTP(rec, authedRequest(http.MethodPost, "/chat", deps.Token, body))

	if len(fd.doCalls) != 1 {
		t.Fatalf("expected exactly one POST /do call, got %d", len(fd.doCalls))
	}
	if fd.doCalls[0].Text != "do something" {
		t.Errorf("Do called with text = %q, want %q", fd.doCalls[0].Text, "do something")
	}
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	wantLocation := "/chat?run=run_2"
	if got := rec.Header().Get("Location"); got != wantLocation {
		t.Errorf("Location = %q, want %q", got, wantLocation)
	}
	// htmx must be told to do a real client-side navigation, not swap the
	// followed redirect's body into this form's small hx-target — see
	// handleChatSend's doc comment.
	if got := rec.Header().Get("HX-Redirect"); got != wantLocation {
		t.Errorf("HX-Redirect = %q, want %q", got, wantLocation)
	}
}

func TestChatSend_continuationPassesContinueRunID(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	fd.doResult = cli.DoResponse{RunID: "run_3", ConversationRunID: "run_1"}
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	body := "text=turn%20two&continueRunId=run_1"
	h.ServeHTTP(rec, authedRequest(http.MethodPost, "/chat", deps.Token, body))

	if len(fd.doCalls) != 1 || fd.doCalls[0].ContinueRunID != "run_1" {
		t.Fatalf("Do calls = %+v, want exactly one call with continueRunId=run_1", fd.doCalls)
	}
	if got := rec.Header().Get("Location"); got != "/chat?run=run_1" {
		t.Errorf("Location = %q, want the conversation's own run, not the new turn's", got)
	}
}
