package web_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/williamokano/kairos/internal/cli"
	"github.com/williamokano/kairos/internal/web"
)

var errBadDefinitionPath = errors.New("no such workflow definition: /not/a/real/path.yaml")

// TestCreateRun_failureRendersAVisibleErrorFragment is a regression test
// for a real bug found via live testing against a genuine claude actor:
// handleCreateRun returned a plain-text http.Error on failure, which htmx
// never swaps into the DOM on a non-2xx response — a real failure (e.g. a
// bad definitionPath) looked exactly like "click run and nothing
// happens." The fix renders an HTML `<p class="error">` fragment instead,
// paired with app.js's htmx:responseError listener that swaps it into the
// triggering element.
func TestCreateRun_failureRendersAVisibleErrorFragment(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	fd.createRunErr = errBadDefinitionPath
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	body := "definitionPath=" + `%2Fnot%2Fa%2Freal%2Fpath.yaml`
	h.ServeHTTP(rec, authedRequest(http.MethodPost, "/runs", deps.Token, body))

	if rec.Code == http.StatusOK {
		t.Fatalf("expected a non-2xx status for a failed run creation, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("error response Content-Type = %q, want text/html so htmx can swap it as a fragment, not plain text it will silently discard", ct)
	}
	if !strings.Contains(rec.Body.String(), `class="error"`) {
		t.Fatalf("error response body = %q, want a real <p class=\"error\"> fragment, not a bare error string", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), errBadDefinitionPath.Error()) {
		t.Errorf("error fragment does not contain the real failure reason: %s", rec.Body.String())
	}
}

// TestRunDetail_rendersNodeOutput is a regression test for a real gap
// found via the same live testing session: the run detail page showed a
// node's status but never its actual output — a user watching a real
// claude actor run had no way to see what it produced anywhere in the web
// UI. Reproduces the exact shape a real llm.actor's node.output.received
// event carries (RunID/NodeID/ExecID/SchemaValid/Output/OutputRef, per
// internal/engine/actor_llm.go).
func TestRunDetail_rendersNodeOutput(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	fd.runState = cli.RunState{
		ID:     "run_1",
		Status: "succeeded",
		Executions: map[string][]cli.NodeExecution{
			"greet": {{ExecID: "greet#a1.i1", NodeID: "greet", Status: "succeeded", Attempt: 1, Iteration: 1}},
		},
	}
	fd.events = []cli.Envelope{
		{StreamID: "run_1", Sequence: 1, EventType: "node.output.received", Event: []byte(
			`{"RunID":"run_1","NodeID":"greet","ExecID":"greet#a1.i1","SchemaValid":true,"Output":{"message":"hello from a real claude actor"},"OutputRef":null}`,
		)},
	}
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodGet, "/runs/run_1", deps.Token, ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "hello from a real claude actor") {
		t.Errorf("run detail page does not render the node's real output anywhere:\n%s", rec.Body.String())
	}
}
