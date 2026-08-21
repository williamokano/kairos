package main_test

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// opsWorkflowPath is a one-node, one-gate workflow real enough to produce
// a genuine constraint.evaluated event (for the events/findings/gates
// pages) and a genuine trigger.received/DefinitionRef (for the flows
// page) — no git workspace needed, unlike diff_compare_webui_test.go's
// fixture.
func opsWorkflowPath(t *testing.T) string {
	t.Helper()
	const yaml = `
name: webui-ops-e2e
gates:
  always-passes:
    kind: command
    waivable: false
    check:
      command: ["true"]
      expect: { exitCode: 0 }
nodes:
  - id: n1
    actor: shell
    prompt: |
      echo '{"ok":true}' > "$KAIROS_OUTPUT_PATH"
    output: { ok: "bool!" }
    gates: [always-passes]
`
	defPath := filepath.Join(t.TempDir(), "webui-ops-e2e.yaml")
	if err := os.WriteFile(defPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}
	return defPath
}

// TestWebUI_operationsPagesAgainstARealDaemon is this stage's own real
// end-to-end proof: the event log explorer (with a real causal tree),
// findings, cost, gates, sources, runners, and flows pages, all driven
// against a real `kairos serve` binary and a real completed run — not
// the fake daemon internal/web's own newpages_test.go already covers in
// isolation.
func TestWebUI_operationsPagesAgainstARealDaemon(t *testing.T) {
	bin := buildKairos(t)
	home := t.TempDir()
	t.Cleanup(func() { stopDaemon(t, home) })

	h := newDaemonHarness(t, bin, home)
	h.start(t, 5*time.Second)
	h.waitForReconciled(t, 3*time.Second)

	tokenBytes, err := os.ReadFile(filepath.Join(home, "web-token"))
	if err != nil {
		t.Fatalf("reading web-token: %v", err)
	}
	token := string(tokenBytes)

	webBase := "http://127.0.0.1:7717"
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}

	resp, err := client.Get(webBase + "/?t=" + token)
	if err != nil {
		t.Fatalf("GET /?t=...: %v", err)
	}
	_ = resp.Body.Close()
	jar := resp.Cookies()
	if len(jar) == 0 {
		t.Fatal("no session cookie set by the one-time exchange")
	}
	get := func(path string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, webBase+path, nil)
		for _, c := range jar {
			req.AddCookie(c)
		}
		r, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer func() { _ = r.Body.Close() }()
		b, _ := io.ReadAll(r.Body)
		return r.StatusCode, string(b)
	}

	// Register a real source (registered/CRUD-real even though no plugin
	// ever successfully polls it in this test) so the Sources page has a
	// real row to render.
	runKairos(t, bin, home, "src", "add", "gh-issues", "--kind", "poll", "--interval", "300")

	runID := h.createRun(t, opsWorkflowPath(t))
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && h.runStatus(t, runID) != "succeeded" {
		if s := h.runStatus(t, runID); s == "failed" {
			t.Fatalf("fixture run failed")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if s := h.runStatus(t, runID); s != "succeeded" {
		t.Fatalf("fixture run status = %q, want succeeded", s)
	}

	// Find the real constraint.evaluated envelope's GlobalSeq for the
	// events explorer's causal tree deep link.
	var gateSeq int64
	for _, e := range h.streamEnvelopes(t, runID) {
		if e.EventType == "constraint.evaluated" {
			gateSeq = e.GlobalSeq
		}
	}
	if gateSeq == 0 {
		t.Fatal("no constraint.evaluated event found in the real run — the fixture's own premise is broken")
	}

	// Events explorer: the unfiltered table shows real rows, and the
	// causal tree for the gate's own event resolves against the real log.
	status, body := get("/events")
	if status != http.StatusOK {
		t.Fatalf("GET /events status = %d, body: %s", status, body)
	}
	if !strings.Contains(body, "constraint.evaluated") {
		t.Errorf("events page missing the real constraint.evaluated row: %s", body)
	}
	status, body = get("/events?focus=" + strconv.FormatInt(gateSeq, 10))
	if status != http.StatusOK {
		t.Fatalf("GET /events?focus=... status = %d, body: %s", status, body)
	}
	if !strings.Contains(body, "causal tree") && !strings.Contains(body, "ancestry") {
		t.Errorf("events page missing the causal tree section: %s", body)
	}

	// Findings: state=all must render the real, passing gate evaluation.
	status, body = get("/findings?state=all")
	if status != http.StatusOK {
		t.Fatalf("GET /findings status = %d, body: %s", status, body)
	}
	if !strings.Contains(body, "always-passes") {
		t.Errorf("findings page missing the real gate evaluation: %s", body)
	}

	// Gates: the real gate must show 1 fire, 0 failures.
	status, body = get("/gates")
	if status != http.StatusOK {
		t.Fatalf("GET /gates status = %d, body: %s", status, body)
	}
	if !strings.Contains(body, "always-passes") {
		t.Errorf("gates page missing the real gate, got: %s", body)
	}

	// Cost: the daemon's real admission-spend estimate (an honest zero,
	// nothing was ever admitted with a cost estimate in this fixture) plus
	// the NL-30 honesty note — never a fabricated number.
	status, body = get("/cost")
	if status != http.StatusOK {
		t.Fatalf("GET /cost status = %d, body: %s", status, body)
	}
	if !strings.Contains(body, "never known") && !strings.Contains(body, "never reconciled") {
		t.Errorf("cost page missing its NL-30 honesty note: %s", body)
	}

	// Sources: the real, just-registered source.
	status, body = get("/sources")
	if status != http.StatusOK {
		t.Fatalf("GET /sources status = %d, body: %s", status, body)
	}
	if !strings.Contains(body, "gh-issues") {
		t.Errorf("sources page missing the real registered source, got: %s", body)
	}

	// Runners: the one real, honest "local" row from real doctor data.
	status, body = get("/runners")
	if status != http.StatusOK {
		t.Fatalf("GET /runners status = %d, body: %s", status, body)
	}
	if !strings.Contains(body, "local") {
		t.Errorf("runners page missing the one real local row, got: %s", body)
	}

	// Flows: the real definition path this run actually used.
	status, body = get("/flows")
	if status != http.StatusOK {
		t.Fatalf("GET /flows status = %d, body: %s", status, body)
	}
	if !strings.Contains(body, "webui-ops-e2e.yaml") {
		t.Errorf("flows page missing the real definition ref, got: %s", body)
	}

	// `kairos cost` itself, not just the API/web layers — the new CLI verb
	// this pass added.
	costOut := runKairos(t, bin, home, "cost")
	if !strings.Contains(costOut, "NL-30") {
		t.Errorf("kairos cost missing its NL-30 honesty note, got: %s", costOut)
	}

	if mismatches := h.dbVerify(t); len(mismatches) != 0 {
		t.Errorf("db verify found mismatches: %v", mismatches)
	}
}
