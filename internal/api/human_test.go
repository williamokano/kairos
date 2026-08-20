package api_test

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/williamokano/kairos/internal/api"
)

func TestHandleApprove_noEngineConfiguredIs503(t *testing.T) {
	deps := api.Deps{Store: nopStore{}} // Engine deliberately left nil
	mux := api.NewMux(deps)

	body, _ := json.Marshal(map[string]string{"nodeId": "approve", "decision": "approve", "reason": "ok"})
	req := httptest.NewRequest("POST", "/runs/run_1/approve", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != 503 {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestHandleApprove_missingFieldsIs400(t *testing.T) {
	deps := api.Deps{Store: nopStore{}}
	mux := api.NewMux(deps)

	req := httptest.NewRequest("POST", "/runs/run_1/approve", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
