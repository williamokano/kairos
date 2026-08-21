package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/williamokano/kairos/internal/cli"
	"github.com/williamokano/kairos/internal/web"
)

func TestComparePage_rendersCostDurationAttemptsFindingsAndDrift(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	fd.compareResult = cli.CompareResult{
		A: cli.CompareSide{RunID: "run_orig", Status: "succeeded", Duration: int64(252 * 1e9), Attempts: 2, Findings: 1},
		B: cli.CompareSide{
			RunID: "run_fork", Status: "succeeded", Duration: int64(408 * 1e9), Attempts: 3, Findings: 3,
			ForkedFrom: "run_orig", Drifted: true, DriftDetail: "requested sequence had no snapshot; restored from a later, live one instead",
		},
	}
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodGet, "/compare?a=run_orig&b=run_fork", deps.Token, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	for _, want := range []string{"run_orig", "run_fork", "succeeded", "4m12s", "6m48s", "forked from"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in the compare page, got: %s", want, body)
		}
	}
	if !strings.Contains(body, "restored from a later, live one instead") {
		t.Errorf("expected the drift detail to be rendered, got: %s", body)
	}
	// This document's own documented reason (NL-30, never durably
	// metered): the compare page must not fabricate a cost figure.
	if strings.Contains(body, "$") {
		t.Errorf("compare page rendered a cost figure the daemon never returned: %s", body)
	}
}

func TestComparePage_missingRunIDsIsABadRequest(t *testing.T) {
	_, sockPath := newFakeDaemon(t)
	deps := testDeps(t, nil, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodGet, "/compare?a=run_orig", deps.Token, ""))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a compare request missing ?b=", rec.Code)
	}
}
