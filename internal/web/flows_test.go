package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/williamokano/kairos/internal/web"
)

// TestCreateFlowDefinition_successRendersDoneFragment proves the web
// editor's happy path reaches deps.Client.CreateFlowDefinition — the
// SAME real registry-validated save path `kairos flow create` uses —
// rather than a parallel, possibly-diverging web-only mechanism.
func TestCreateFlowDefinition_successRendersDoneFragment(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	body := "name=my-flow&yaml=" + "name%3A+my-flow%0Anodes%3A+%5B%5D"
	h.ServeHTTP(rec, authedRequest(http.MethodPost, "/flows/new", deps.Token, body))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if len(fd.createFlowCalls) != 1 || fd.createFlowCalls[0].Name != "my-flow" {
		t.Fatalf("createFlowCalls = %+v, want exactly one for 'my-flow'", fd.createFlowCalls)
	}
	if !strings.Contains(rec.Body.String(), "saved") {
		t.Errorf("expected a real success fragment, got: %s", rec.Body.String())
	}
}

// TestCreateFlowDefinition_failureRendersVisibleError is the same class
// of regression the composer bug fix already established (a plain
// http.Error is invisible to htmx on non-2xx): the real registry.Load
// error must render as a visible HTML fragment, not a swallowed string.
func TestCreateFlowDefinition_failureRendersVisibleError(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	fd.createFlowErr = errBadDefinitionPath
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	body := "name=bad&yaml=not+valid"
	h.ServeHTTP(rec, authedRequest(http.MethodPost, "/flows/new", deps.Token, body))

	if rec.Code == http.StatusOK {
		t.Fatalf("expected a non-2xx status for a rejected workflow, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("error response Content-Type = %q, want text/html so htmx can swap it", ct)
	}
	if !strings.Contains(rec.Body.String(), errBadDefinitionPath.Error()) {
		t.Errorf("error fragment does not contain the real failure reason: %s", rec.Body.String())
	}
}

// TestCreateCronSource_buildsIdenticalConfigToTheCLIPath proves the web
// form's structured fields produce the EXACT config string
// tasksource.BuildCronConfig renders — the same shape `kairos src add
// cron`'s friendly flags build — never a second, divergent schema.
func TestCreateCronSource_buildsIdenticalConfigToTheCLIPath(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	body := "id=nightly&schedule=daily&hour=3&minute=30&flow=%2Fhome%2Fyou%2Fflow.yaml"
	h.ServeHTTP(rec, authedRequest(http.MethodPost, "/sources/new-cron", deps.Token, body))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if len(fd.addSourceCalls) != 1 {
		t.Fatalf("addSourceCalls = %+v, want exactly one", fd.addSourceCalls)
	}
	call := fd.addSourceCalls[0]
	if call.Kind != "cron" {
		t.Errorf("kind = %q, want cron", call.Kind)
	}
	wantConfig := `{"schedule":"daily","hour":3,"minute":30}`
	if call.Config != wantConfig {
		t.Errorf("config = %s, want %s", call.Config, wantConfig)
	}
}

func TestCreateCronSource_missingRequiredFieldRendersVisibleError(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	body := "id=nightly&schedule=daily" // missing flow
	h.ServeHTTP(rec, authedRequest(http.MethodPost, "/sources/new-cron", deps.Token, body))

	if rec.Code == http.StatusOK {
		t.Fatalf("expected a non-2xx status for a missing required field, got %d", rec.Code)
	}
	if len(fd.addSourceCalls) != 0 {
		t.Errorf("expected no AddSource call for an incomplete form, got %+v", fd.addSourceCalls)
	}
}
