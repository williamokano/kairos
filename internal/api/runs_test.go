package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/api"
	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/events"
	"github.com/williamokano/kairos/internal/eventstore"
)

func openTestStore(t *testing.T) eventstore.Store {
	t.Helper()
	registry, err := events.Builtin()
	if err != nil {
		t.Fatalf("Builtin: %v", err)
	}
	st, err := eventstore.Open(context.Background(), eventstore.Config{
		Path:     filepath.Join(t.TempDir(), "kairos.db"),
		Registry: registry,
		Projections: []eventstore.Projection{
			eventstore.RunStateProjection{},
			eventstore.RunIndexProjection{},
		},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// fixIssuePath resolves internal/registry's fix-issue.yaml fixture, reused
// here rather than duplicated — L03's canonical example.
func fixIssuePath(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("../registry/testdata/fix-issue.yaml")
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	return abs
}

func TestCreateRun_appendsTriggerReceivedAndRunStarted(t *testing.T) {
	store := openTestStore(t)
	deps := api.Deps{Store: store, StartedAt: time.Now()}
	mux := api.NewMux(deps)

	body, _ := json.Marshal(map[string]any{"definitionPath": fixIssuePath(t)})
	req := httptest.NewRequest("POST", "/runs", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != 201 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		RunID  string `json:"runId"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.RunID == "" {
		t.Fatal("expected a non-empty runId")
	}
	if resp.Status != string(domain.RunRunning) {
		t.Errorf("Status = %q, want %q", resp.Status, domain.RunRunning)
	}

	envs, err := store.Read(context.Background(), resp.RunID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(envs) != 2 {
		t.Fatalf("len(envs) = %d, want 2 (trigger.received, run.started)", len(envs))
	}
	if envs[0].EventType != "trigger.received" || envs[1].EventType != "run.started" {
		t.Errorf("event types = %s, %s", envs[0].EventType, envs[1].EventType)
	}
}

func TestCreateRun_rejectsAnInvalidDefinitionWith422(t *testing.T) {
	store := openTestStore(t)
	deps := api.Deps{Store: store}
	mux := api.NewMux(deps)

	body, _ := json.Marshal(map[string]any{"definitionPath": "/no/such/file.yaml"})
	req := httptest.NewRequest("POST", "/runs", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != 422 {
		t.Fatalf("status = %d, want 422; body = %s", rec.Code, rec.Body.String())
	}
}

func TestGetRun_returns404ForUnknownRun(t *testing.T) {
	store := openTestStore(t)
	deps := api.Deps{Store: store}
	mux := api.NewMux(deps)

	req := httptest.NewRequest("GET", "/runs/no-such-run", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != 404 {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestListRuns_reflectsCreatedRuns(t *testing.T) {
	store := openTestStore(t)
	deps := api.Deps{Store: store}
	mux := api.NewMux(deps)

	body, _ := json.Marshal(map[string]any{"definitionPath": fixIssuePath(t)})
	req := httptest.NewRequest("POST", "/runs", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("create: status = %d", rec.Code)
	}

	listReq := httptest.NewRequest("GET", "/runs", nil)
	listRec := httptest.NewRecorder()
	mux.ServeHTTP(listRec, listReq)
	if listRec.Code != 200 {
		t.Fatalf("list: status = %d", listRec.Code)
	}
	var resp struct {
		Runs []eventstore.RunSummary `json:"runs"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(resp.Runs) != 1 {
		t.Fatalf("len(Runs) = %d, want 1", len(resp.Runs))
	}
}
