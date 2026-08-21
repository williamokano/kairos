package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/williamokano/kairos/internal/cli"
	"github.com/williamokano/kairos/internal/web"
)

// TestRunPage_dangerZoneDialogsRenderClosedAndDisabled proves the same
// anti-rubber-stamp shape the decision page established: the cancel/fork
// dialogs exist, but neither is open by default, and each submit button
// renders `disabled` — enabled only by app.js once the typed confirm
// field matches (a client-side UX aid; see the bypass tests below for the
// server-side invariant that actually holds).
func TestRunPage_dangerZoneDialogsRenderClosedAndDisabled(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	fd.runState = cli.RunState{ID: "run_1", Status: "running"}
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodGet, "/runs/run_1", deps.Token, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	for _, id := range []string{"cancel-dialog", "fork-dialog"} {
		idx := strings.Index(body, `id="`+id+`"`)
		if idx == -1 {
			t.Fatalf("dialog %q not found in rendered page", id)
		}
	}
	if strings.Contains(body, `<dialog id="cancel-dialog" class="confirm-dialog" open`) {
		t.Error("cancel dialog must not render open by default")
	}
	// Every data-confirm-submit button in the danger zone must render
	// disabled — app.js is the only thing that ever enables it, and only
	// once the typed field matches.
	count := strings.Count(body, "data-confirm-submit disabled")
	if count != 2 {
		t.Errorf("expected 2 disabled-by-default confirm-submit buttons (cancel, fork), found %d", count)
	}
}

// TestCancelDialog_bypassWithoutMatchingConfirmIsRejected mirrors
// TestDecisionAnswer_reachesTheSameServerValidationAsApprove's discipline
// for the new cancel dialog: a raw POST that skips the dialog and its
// typed-confirm field entirely (exactly what a devtools-edited or
// script-driven bypass would send) must be rejected server-side, and must
// never reach the daemon's Cancel call.
func TestCancelDialog_bypassWithoutMatchingConfirmIsRejected(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	cases := []struct {
		name string
		body string
	}{
		{"no confirm field at all", "reason=done"},
		{"confirm field present but wrong", "reason=done&confirm=not-the-run-id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fd.cancelCalls = nil
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, authedRequest(http.MethodPost, "/runs/run_1/cancel", deps.Token, tc.body))
			if rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("status = %d, want 422 (bypass must be rejected)", rec.Code)
			}
			if len(fd.cancelCalls) != 0 {
				t.Errorf("expected the daemon's Cancel to never be called on a bypass attempt, got %d calls", len(fd.cancelCalls))
			}
		})
	}
}

// TestCancelDialog_matchingConfirmReachesTheDaemon proves the positive
// path: with the exact confirm text (what the dialog's own JS would only
// ever allow submitting), the request reaches deps.Client.Cancel with the
// right runID and reason.
func TestCancelDialog_matchingConfirmReachesTheDaemon(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodPost, "/runs/run_1/cancel", deps.Token, "reason=no+longer+needed&confirm=run_1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if len(fd.cancelCalls) != 1 {
		t.Fatalf("expected exactly one Cancel call, got %d", len(fd.cancelCalls))
	}
	if fd.cancelCalls[0].RunID != "run_1" || fd.cancelCalls[0].Reason != "no longer needed" {
		t.Errorf("unexpected cancel call: %+v", fd.cancelCalls[0])
	}
}

// TestForkDialog_bypassWithoutMatchingConfirmIsRejected is the fork
// dialog's version of the same invariant.
func TestForkDialog_bypassWithoutMatchingConfirmIsRejected(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodPost, "/runs/run_1/fork", deps.Token, "allowDrift=on"))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rec.Code)
	}
	if len(fd.forkCalls) != 0 {
		t.Errorf("expected Fork to never be called on a bypass attempt, got %d calls", len(fd.forkCalls))
	}
}

func TestForkDialog_matchingConfirmReachesTheDaemon(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	fd.forkResult = cli.ForkResult{NewRunID: "run_2"}
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodPost, "/runs/run_1/fork", deps.Token, "confirm=run_1&atSequence=5&allowDrift=on"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if len(fd.forkCalls) != 1 {
		t.Fatalf("expected exactly one Fork call, got %d", len(fd.forkCalls))
	}
	call := fd.forkCalls[0]
	if call.RunID != "run_1" || call.AtSequence != 5 || !call.AllowDrift {
		t.Errorf("unexpected fork call: %+v", call)
	}
	if !strings.Contains(rec.Body.String(), "run_2") {
		t.Errorf("expected the new forked run id in the response, got: %s", rec.Body.String())
	}
}

// TestPauseSourceDialog_bypassWithoutMatchingConfirmIsRejected and its
// positive-path sibling are the source-pause dialog's version.
func TestPauseSourceDialog_bypassWithoutMatchingConfirmIsRejected(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodPost, "/sources/gh-issues/pause", deps.Token, ""))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rec.Code)
	}
	if len(fd.pauseCalls) != 0 {
		t.Errorf("expected PauseSource to never be called on a bypass attempt, got %d calls", len(fd.pauseCalls))
	}
}

func TestPauseSourceDialog_matchingConfirmReachesTheDaemon(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodPost, "/sources/gh-issues/pause", deps.Token, "confirm=gh-issues"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if len(fd.pauseCalls) != 1 || fd.pauseCalls[0] != "gh-issues" {
		t.Fatalf("expected exactly one PauseSource(gh-issues) call, got %v", fd.pauseCalls)
	}
}

// TestSourcesPage_pauseDialogOnlyForEnabledSources proves the dialog only
// renders for a source that is actually still pollable — pausing an
// already-paused source has no daemon meaning.
func TestSourcesPage_pauseDialogOnlyForEnabledSources(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	fd.sources = []cli.Source{
		{ID: "enabled-src", Kind: "poll", Enabled: true},
		{ID: "paused-src", Kind: "poll", Enabled: false},
	}
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodGet, "/sources", deps.Token, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="pause-dialog-enabled-src"`) {
		t.Error("expected a pause dialog for the enabled source")
	}
	if strings.Contains(body, `id="pause-dialog-paused-src"`) {
		t.Error("did not expect a pause dialog for an already-paused source")
	}
}
