package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/williamokano/kairos/internal/cli"
	"github.com/williamokano/kairos/internal/web"
)

func TestEventsPage_rendersFilteredRowsAndCausalTree(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	seq1 := int64(100)
	fd.events = []cli.Envelope{
		{StreamID: "run_1", GlobalSeq: 100, Sequence: 1, EventType: "node.execution.started", Event: []byte(`{}`)},
		{StreamID: "run_1", GlobalSeq: 101, Sequence: 2, EventType: "constraint.evaluated", Event: []byte(`{"GateID":"lint","Passed":true}`), CausationSeq: &seq1},
		{StreamID: "run_1", GlobalSeq: 102, Sequence: 3, EventType: "node.execution.succeeded", Event: []byte(`{}`), CausationSeq: &[]int64{101}[0]},
	}
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	// The plain, unfiltered table.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodGet, "/events", deps.Token, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"100", "101", "102", "constraint.evaluated", "node.execution.started"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in the unfiltered events table, got: %s", want, body)
		}
	}

	// type=constraint.evaluated narrows the table but the causal tree
	// still needs the full set — focus on 102 must still show its real
	// ancestor (101), which the type filter alone would have excluded.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodGet, "/events?type=constraint.evaluated&focus=102", deps.Token, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body = rec.Body.String()
	if strings.Contains(body, "node.execution.succeeded") == false {
		t.Errorf("expected the focused event (102, node.execution.succeeded) in the causal tree, got: %s", body)
	}
	if !strings.Contains(body, "constraint.evaluated") {
		t.Errorf("expected the ancestor (101) in the causal tree despite the type filter narrowing the table, got: %s", body)
	}
	if !strings.Contains(body, "node.execution.started") {
		t.Errorf("expected the root ancestor (100) in the causal tree, got: %s", body)
	}
}

func TestFindingsPage_defaultsToFailedOnly(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	fd.events = []cli.Envelope{
		{StreamID: "run_1", GlobalSeq: 1, EventType: "constraint.evaluated", Event: []byte(`{"RunID":"run_1","NodeID":"n1","GateID":"lint","Passed":true,"Reason":"clean"}`)},
		{StreamID: "run_1", GlobalSeq: 2, EventType: "constraint.evaluated", Event: []byte(`{"RunID":"run_1","NodeID":"n1","GateID":"security","Passed":false,"Reason":"hardcoded secret"}`)},
	}
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodGet, "/findings", deps.Token, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "hardcoded secret") {
		t.Errorf("expected the failing finding, got: %s", body)
	}
	if strings.Contains(body, "clean") {
		t.Errorf("default state=failed must not render the passing gate's reason, got: %s", body)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodGet, "/findings?state=all", deps.Token, ""))
	body = rec.Body.String()
	if !strings.Contains(body, "clean") || !strings.Contains(body, "hardcoded secret") {
		t.Errorf("state=all must render both findings, got: %s", body)
	}
}

func TestGatesPage_computesFiresFailuresAndWaivers(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	fd.events = []cli.Envelope{
		{StreamID: "run_1", GlobalSeq: 1, EventType: "constraint.evaluated", Event: []byte(`{"RunID":"run_1","NodeID":"n1","GateID":"lint","Kind":"command","Passed":true}`)},
		{StreamID: "run_1", GlobalSeq: 2, EventType: "constraint.evaluated", Event: []byte(`{"RunID":"run_1","NodeID":"n1","GateID":"lint","Kind":"command","Passed":false}`)},
		{StreamID: "run_1", GlobalSeq: 3, EventType: "waiver.grant", Event: []byte(`{"RunID":"run_1","NodeID":"n1","GateID":"lint"}`)},
	}
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodGet, "/gates", deps.Token, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"lint", "2", "1", "50%"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in the gates page (2 fires, 1 failure, 50%% catch rate), got: %s", want, body)
		}
	}
}

func TestCostPage_rendersEstimateAndCapHonestly(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	fd.costResp = cli.CostResponse{Day: "2026-08-21", SpentUSD: 1.84, SpendRecorded: true, DailyCapUSD: 25}
	fd.events = []cli.Envelope{
		{StreamID: "run_1", GlobalSeq: 1, EventType: "session.cost.unavailable", Event: []byte(`{"RunID":"run_1","NodeID":"n1"}`)},
	}
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodGet, "/cost", deps.Token, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"2026-08-21", "$1.84", "$25.00", "1 execution"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q on the cost page, got: %s", want, body)
		}
	}
	if !strings.Contains(body, "never known") && !strings.Contains(body, "never reconciled") {
		t.Errorf("expected the NL-30 honesty note on the cost page, got: %s", body)
	}
}

func TestSourcesPage_rendersHealthCursorAndConsecutiveErrors(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	lastPoll := "2026-08-21T10:00:00Z"
	fd.sources = []cli.Source{
		{ID: "gh-issues", Kind: "poll", Health: "unhealthy", HealthReason: "401 unauthorized",
			ConsecutiveErrors: 3, LastPollAt: &lastPoll, Cursor: "issue_492"},
	}
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodGet, "/sources", deps.Token, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"gh-issues", "unhealthy", "401 unauthorized", "3", "issue_492"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q on the sources page, got: %s", want, body)
		}
	}
}

func TestRunnersPage_rendersOneHonestLocalRow(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodGet, "/runners", deps.Token, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "local") {
		t.Errorf("expected the one real 'local' runner row, got: %s", body)
	}
	if strings.Contains(body, "macmini") || strings.Contains(body, "remote") {
		t.Errorf("runners page must not invent a runner no endpoint provides, got: %s", body)
	}
}

func TestFlowsPage_groupsRunsByDefinitionRef(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	fd.events = []cli.Envelope{
		{StreamID: "run_a", GlobalSeq: 1, EventType: "trigger.received", Event: []byte(`{"RunID":"run_a","DefinitionRef":"flows/implement.yaml"}`)},
		{StreamID: "run_b", GlobalSeq: 2, EventType: "trigger.received", Event: []byte(`{"RunID":"run_b","DefinitionRef":"flows/implement.yaml"}`)},
		{StreamID: "run_c", GlobalSeq: 3, EventType: "trigger.received", Event: []byte(`{"RunID":"run_c","DefinitionRef":"flows/adhoc.yaml"}`)},
	}
	fd.runs = []cli.RunSummary{
		{RunID: "run_a", Status: "succeeded"},
		{RunID: "run_b", Status: "failed"},
		{RunID: "run_c", Status: "succeeded"},
	}
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodGet, "/flows", deps.Token, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"flows/implement.yaml", "flows/adhoc.yaml"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q grouped on the flows page, got: %s", want, body)
		}
	}
}
