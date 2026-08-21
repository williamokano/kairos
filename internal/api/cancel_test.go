package api_test

import (
	"bytes"
	"net/http/httptest"
	"testing"

	"github.com/williamokano/kairos/internal/api"
)

// TestHandleCancel_noEngineConfiguredIs503 mirrors
// TestHandleApprove_noEngineConfiguredIs503 for the new POST
// /runs/{id}/cancel route (internal/engine/cancel.go) — the daemon-side
// capability this pass built where none existed before (see
// L23-webui-revamp.md).
func TestHandleCancel_noEngineConfiguredIs503(t *testing.T) {
	deps := api.Deps{Store: nopStore{}} // Engine deliberately left nil
	mux := api.NewMux(deps)

	req := httptest.NewRequest("POST", "/runs/run_1/cancel", bytes.NewReader([]byte(`{"reason":"stop"}`)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != 503 {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// TestHandleCancel_malformedBodyIs400 proves a genuinely broken request
// body is rejected before ever reaching the engine.
func TestHandleCancel_malformedBodyIs400(t *testing.T) {
	deps := api.Deps{Store: nopStore{}}
	mux := api.NewMux(deps)

	req := httptest.NewRequest("POST", "/runs/run_1/cancel", bytes.NewReader([]byte(`{not json`)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
