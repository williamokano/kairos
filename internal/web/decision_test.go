package web_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/williamokano/kairos/internal/cli"
	"github.com/williamokano/kairos/internal/web"
)

func authedRequest(method, path, token, body string) *http.Request {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Host = "kairos.test"
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

// TestDecisionPage_fieldsetDisabledByDefault is this document's central
// safety-property test: the decision form must render with its fieldset
// disabled by default, evidence panes before the form in DOM order, and
// no way to submit without the client-side JS running (which app.js's own
// logic conditions on IntersectionObserver-confirmed pane views) — and
// even then, only the server's independent check at POST .../answer
// actually decides (see TestDecisionAnswer_serverEnforcesTypedWordRegardlessOfFieldOrder).
func TestDecisionPage_fieldsetDisabledByDefaultAndEvidenceBeforeForm(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	fd.events = []cli.Envelope{
		{StreamID: "run_1", Sequence: 1, EventType: "constraint.evaluated", Event: []byte(`{"NodeID":"approve","GateID":"lint","Passed":true}`)},
	}
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodGet, "/t/run_1/approve", deps.Token, ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	fieldsetIdx := strings.Index(body, `id="decision-fieldset"`)
	if fieldsetIdx == -1 {
		t.Fatal("decision fieldset not found in rendered page")
	}
	// "disabled" must appear on the fieldset tag itself, not merely
	// somewhere later in the document.
	tagEnd := strings.Index(body[fieldsetIdx:], ">")
	if tagEnd == -1 || !strings.Contains(body[fieldsetIdx:fieldsetIdx+tagEnd], "disabled") {
		t.Error("decision fieldset does not render with the disabled attribute by default")
	}

	riskIdx := strings.Index(body, `data-pane="risk"`)
	findingsIdx := strings.Index(body, `data-pane="findings"`)
	formIdx := strings.Index(body, `data-pane="decision"`)
	if riskIdx == -1 || findingsIdx == -1 || formIdx == -1 {
		t.Fatal("expected risk/findings/decision panes all present")
	}
	if riskIdx >= findingsIdx || findingsIdx >= formIdx {
		t.Errorf("evidence-before-controls DOM order violated: risk=%d findings=%d decision=%d", riskIdx, findingsIdx, formIdx)
	}
}

// TestDecisionPage_evidenceLoadFailureBlocksForm proves the "failed
// evidence blocks the form" rule: when the daemon can't be reached for
// event history, the page must render the blocked state, not a decision
// form with stale/empty evidence.
func TestDecisionPage_evidenceLoadFailureBlocksForm(t *testing.T) {
	_, sockPath := newFakeDaemon(t)
	deps := testDeps(t, nil, sockPath)
	// Point at a socket path that doesn't exist, so the events fetch fails.
	deps.Client = cli.NewClient(sockPath + ".does-not-exist")
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodGet, "/t/run_1/approve", deps.Token, ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "decision-blocked") {
		t.Error("expected the decision-blocked state when evidence fails to load")
	}
	if strings.Contains(body, `id="decision-fieldset"`) {
		t.Error("the decision form must not render at all when evidence failed to load")
	}
}

// TestDecisionAnswer_reachesTheSameServerValidationAsApprove proves the
// web form is not a parallel, weaker approval path: it posts through to
// the exact same POST /runs/{id}/approve the CLI and TUI use, and a
// server-side rejection (e.g. reason required) surfaces here too.
func TestDecisionAnswer_reachesTheSameServerValidationAsApprove(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	fd.approveErr = fmt.Errorf("engine: a reason is required for every decision")
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	body := "decision=approve&reason=&typedWord=approve&nonce=x"
	h.ServeHTTP(rec, authedRequest(http.MethodPost, "/t/run_1/approve/answer", deps.Token, body))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 (the daemon's rejection must surface, not be swallowed)", rec.Code)
	}
	if len(fd.approveCalls) != 1 {
		t.Fatalf("expected exactly one approve call reaching the daemon, got %d", len(fd.approveCalls))
	}
	if fd.approveCalls[0].RunID != "run_1" || fd.approveCalls[0].NodeID != "approve" {
		t.Errorf("unexpected approve call: %+v", fd.approveCalls[0])
	}
}

// TestNoBulkApproveRouteExists is a real, structural check — not merely
// "we didn't build one" but "the route table cannot resolve one" — for
// every plausible bulk/global-approve path name.
func TestNoBulkApproveRouteExists(t *testing.T) {
	deps := web.Deps{Token: "x", AllowedHosts: []string{"kairos.test"}}
	mux := web.NewRawMux(deps)

	forbidden := []string{
		"/approve-all", "/answer-all", "/t/approve-all", "/human-tasks/answer",
		"/approve", "/decisions/approve-all", "/bulk-approve",
	}
	for _, path := range forbidden {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		if _, pattern := mux.Handler(req); pattern != "" {
			t.Errorf("found a route for %q (pattern %q) — no bulk/global approve path may exist", path, pattern)
		}
	}
}
