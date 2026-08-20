package tasksource_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/williamokano/kairos/internal/tasksource"
)

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestWebhookHandler_badSignatureIsRejected(t *testing.T) {
	st := openStore(t)
	h := tasksource.Handler(tasksource.WebhookConfig{
		SourceID: "gh-hook", Secret: "whsec_real",
		Parse: func(body []byte) (tasksource.WorkItem, error) {
			return tasksource.WorkItem{ID: "1", DedupeKey: "wh-1"}, nil
		},
		DefaultFlow: demoFlow(t),
	}, st)

	body := []byte(`{"x":1}`)
	req := httptest.NewRequest("POST", "/v1/hook/gh-hook", bytes.NewReader(body))
	req.Header.Set("X-Kairos-Signature", "not-the-right-signature")
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (no body distinguishing bad sig from unknown source)", rec.Code)
	}
	runs, err := st.ListRuns(req.Context(), nil)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Error("a bad signature must never create a run")
	}
}

func TestWebhookHandler_validSignatureCreatesARun(t *testing.T) {
	st := openStore(t)
	secret := "whsec_real"
	h := tasksource.Handler(tasksource.WebhookConfig{
		SourceID: "gh-hook", Secret: secret,
		Parse: func(body []byte) (tasksource.WorkItem, error) {
			return tasksource.WorkItem{ID: "1", DedupeKey: "wh-valid-1"}, nil
		},
		DefaultFlow: demoFlow(t),
	}, st)

	body := []byte(`{"x":1}`)
	req := httptest.NewRequest("POST", "/v1/hook/gh-hook", bytes.NewReader(body))
	req.Header.Set("X-Kairos-Signature", sign(secret, body))
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	runs, err := st.ListRuns(req.Context(), nil)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("len(runs) = %d, want 1", len(runs))
	}
}

func TestWebhookHandler_redeliveryProducesExactlyOneRun(t *testing.T) {
	st := openStore(t)
	secret := "whsec_real"
	h := tasksource.Handler(tasksource.WebhookConfig{
		SourceID: "gh-hook", Secret: secret,
		Parse: func(body []byte) (tasksource.WorkItem, error) {
			return tasksource.WorkItem{ID: "1", DedupeKey: "wh-redelivered"}, nil
		},
		DefaultFlow: demoFlow(t),
	}, st)

	body := []byte(`{"x":1}`)
	sig := sign(secret, body)

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("POST", "/v1/hook/gh-hook", bytes.NewReader(body))
		req.Header.Set("X-Kairos-Signature", sig)
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("delivery %d: status = %d, want 202", i, rec.Code)
		}
	}

	runs, err := st.ListRuns(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("len(runs) = %d, want exactly 1 despite 3 redeliveries", len(runs))
	}
}
