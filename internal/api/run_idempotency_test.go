package api_test

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/api"
)

// TestCreateRun_idempotencyKeyDedupesADoubleSubmit is NL-49's enforcing
// test (11-limitations.md): two POST /runs calls carrying the same
// Idempotency-Key must produce exactly one run, not two — the web
// composer's "nonce" form field was already minted and posted before
// this fix, but silently ignored server-side.
func TestCreateRun_idempotencyKeyDedupesADoubleSubmit(t *testing.T) {
	store := openTestStore(t)
	deps := api.Deps{Store: store, StartedAt: time.Now()}
	mux := api.NewMux(deps)

	post := func(key string) (int, struct {
		RunID  string `json:"runId"`
		Status string `json:"status"`
	}) {
		body, _ := json.Marshal(map[string]any{"definitionPath": fixIssuePath(t), "idempotencyKey": key})
		req := httptest.NewRequest("POST", "/runs", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var resp struct {
			RunID  string `json:"runId"`
			Status string `json:"status"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp
	}

	status1, first := post("key-abc")
	if status1 != 201 {
		t.Fatalf("first POST status = %d, want 201", status1)
	}
	if first.RunID == "" {
		t.Fatal("first POST: expected a non-empty runId")
	}

	status2, second := post("key-abc")
	if status2 != 200 {
		t.Fatalf("second POST (same key) status = %d, want 200 (existing run returned, not a new 201)", status2)
	}
	if second.RunID != first.RunID {
		t.Fatalf("second POST returned a different run: %q, want the first call's %q — the double-submit was not deduped", second.RunID, first.RunID)
	}

	// A DIFFERENT key must still create a genuinely new run — the dedupe
	// must not become a blanket "reuse the last run" bug.
	status3, third := post("key-xyz")
	if status3 != 201 {
		t.Fatalf("third POST (different key) status = %d, want 201", status3)
	}
	if third.RunID == first.RunID {
		t.Fatal("a different idempotency key returned the same run — dedupe is over-matching")
	}

	// No key at all must behave exactly as before this fix: every call
	// creates a new run.
	status4, fourth := post("")
	status5, fifth := post("")
	if status4 != 201 || status5 != 201 {
		t.Fatalf("no-key POSTs: statuses = %d, %d, want 201, 201 (no dedupe without a key)", status4, status5)
	}
	if fourth.RunID == fifth.RunID {
		t.Fatal("two no-key POSTs returned the same run — dedupe must not apply when no key is supplied")
	}
}
