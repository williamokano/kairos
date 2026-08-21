package main_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestIntegration_postRunsIdempotencyKeyDedupesOverRealHTTP is the
// real-daemon proof for NL-49 (11-limitations.md): internal/api's own
// httptest-driven test already proves the dedupe logic in isolation;
// this drives it over the actual unix socket against a real running
// daemon, matching kill_mid_run_test.go's direct-HTTP style (the CLI
// itself doesn't expose an --idempotency-key flag on `kairos run` — this
// exercises the web composer's exact code path, POST /runs with an
// idempotencyKey field in the JSON body).
func TestIntegration_postRunsIdempotencyKeyDedupesOverRealHTTP(t *testing.T) {
	bin := buildKairos(t)
	home := t.TempDir()
	t.Cleanup(func() { stopDaemon(t, home) })

	h := newDaemonHarness(t, bin, home)
	h.start(t, 5*time.Second)

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", h.sockPath)
			},
		},
		Timeout: 10 * time.Second,
	}

	defPath, err := filepath.Abs(milestoneNoAdoptPath(t))
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}

	post := func(key string) (int, string) {
		body, _ := json.Marshal(map[string]any{"definitionPath": defPath, "idempotencyKey": key})
		resp, err := client.Post("http://kairos/runs", "application/json", strings.NewReader(string(body)))
		if err != nil {
			t.Fatalf("POST /runs: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out struct {
			RunID string `json:"runId"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out.RunID
	}

	status1, runID1 := post("integration-key-1")
	if status1 != 201 || runID1 == "" {
		t.Fatalf("first POST: status=%d runID=%q, want 201 and a non-empty id", status1, runID1)
	}

	status2, runID2 := post("integration-key-1")
	if status2 != 200 {
		t.Fatalf("second POST (same key): status = %d, want 200", status2)
	}
	if runID2 != runID1 {
		t.Fatalf("second POST returned run %q, want the first call's %q — a real double-submit created two runs", runID2, runID1)
	}
}
