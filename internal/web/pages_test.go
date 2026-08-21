package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/williamokano/kairos/internal/cli"
	"github.com/williamokano/kairos/internal/web"
)

func TestHomePage_rendersRealRunData(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	fd.runs = []cli.RunSummary{
		{RunID: "run_running1", Status: "running", StartedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z"},
	}
	fd.runState = cli.RunState{ID: "run_running1", Status: "running"}
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodGet, "/", deps.Token, ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "run_running") {
		t.Errorf("expected the running run to appear on the home page, got: %s", rec.Body.String())
	}
}

// TestHomePage_waitingOnYouReadsTheRealIndex is L20-webui.md's Documented
// decision #5's fix: the home page's "waiting on you" section must come
// from GET /human-tasks (HumanTaskIndexProjection's real index), not a
// per-run GetRun scan — proven here by a run whose fake runState carries
// NO waiting execution at all, yet still appears because the fake
// daemon's /human-tasks endpoint says it's open. Before this fix, the
// item would never have appeared (the old code never called this
// endpoint at all).
func TestHomePage_waitingOnYouReadsTheRealIndex(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	fd.runs = []cli.RunSummary{
		{RunID: "run_needs_you", Status: "running", StartedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z"},
	}
	fd.runState = cli.RunState{ID: "run_needs_you", Status: "running"} // no Executions at all
	fd.humanTasks = []cli.OpenHumanTask{
		{RunID: "run_needs_you", NodeID: "approve", Kind: "human", OpenedAt: "2026-01-01T00:00:00Z"},
	}
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodGet, "/", deps.Token, ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "run_needs_you") || !strings.Contains(body, "approve") {
		t.Errorf("expected the indexed waiting task (run_needs_you/approve) to appear on the home page, got: %s", body)
	}
}

func TestRunsListPage_rendersEmptyState(t *testing.T) {
	_, sockPath := newFakeDaemon(t)
	deps := testDeps(t, nil, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodGet, "/runs", deps.Token, ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "No runs yet") {
		t.Error("expected the documented empty-state message, not a bare blank table")
	}
}

func TestStaticAssets_htmxServedWithCorrectType(t *testing.T) {
	_, sockPath := newFakeDaemon(t)
	deps := testDeps(t, nil, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodGet, "/static/htmx.min.js", deps.Token, ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Body.Len() < 1000 {
		t.Errorf("htmx.min.js body suspiciously small (%d bytes) — is it actually vendored?", rec.Body.Len())
	}
}

func TestConversationPage_postAndRenderRoundTrip(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodPost, "/c/run_1/messages", deps.Token, "text=hello+there&nonce=x"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "hello there") {
		t.Errorf("expected the posted message echoed back, got: %s", rec.Body.String())
	}
}
