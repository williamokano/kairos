package main_test

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestWebUI_realDaemonServesHomeAndAnswersADecision drives the actual
// built binary — real `kairos serve`, real web listener, real
// $KAIROS_HOME/web-token — through the one-time token exchange, the home
// page, and a full decision-answer round trip via the web form, proving
// L20's web UI is wired into the daemon for real, not just unit-tested
// against a fake backend (internal/web's own tests, package web_test,
// cover the auth/rendering logic in isolation; this proves the
// composition root actually wires it all together).
func TestWebUI_realDaemonServesHomeAndAnswersADecision(t *testing.T) {
	bin := buildKairos(t)
	home := t.TempDir()

	h := newDaemonHarness(t, bin, home)
	h.start(t, 5*time.Second)
	h.waitForReconciled(t, 3*time.Second)

	tokenBytes, err := os.ReadFile(filepath.Join(home, "web-token"))
	if err != nil {
		t.Fatalf("reading web-token: %v", err)
	}
	token := string(tokenBytes)
	if len(token) != 64 { // 32 bytes hex-encoded
		t.Errorf("web token length = %d, want 64 (32 bytes hex)", len(token))
	}

	webBase := "http://127.0.0.1:7717"
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
	}

	// The one-time exchange.
	resp, err := client.Get(webBase + "/?t=" + token)
	if err != nil {
		t.Fatalf("GET /?t=...: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("one-time exchange status = %d, want 302", resp.StatusCode)
	}
	var jar []*http.Cookie
	jar = append(jar, resp.Cookies()...)
	if len(jar) == 0 {
		t.Fatal("no session cookie set by the one-time exchange")
	}

	get := func(path string) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, webBase+path, nil)
		for _, c := range jar {
			req.AddCookie(c)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		return resp
	}

	homeResp := get("/")
	homeBody, _ := io.ReadAll(homeResp.Body)
	_ = homeResp.Body.Close()
	if homeResp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d", homeResp.StatusCode)
	}
	if !strings.Contains(string(homeBody), "path to a workflow definition") {
		t.Errorf("home page does not look like the real page: %s", string(homeBody))
	}

	// Run the human-approval fixture, then answer its decision through
	// the web form exactly as a browser would.
	runID := h.createRun(t, milestoneApprovalDefPath(t))

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && h.runStatus(t, runID) != "waiting" && h.runStatus(t, runID) != "running" {
		time.Sleep(50 * time.Millisecond)
	}
	// Poll until the approve node is actually waiting.
	var waitingNode string
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		envs := h.streamEnvelopes(t, runID)
		for _, e := range envs {
			if e.EventType == "node.execution.started" {
				waitingNode = "approve"
			}
		}
		if waitingNode != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if waitingNode == "" {
		t.Fatal("approve node never started")
	}
	time.Sleep(500 * time.Millisecond) // let it actually reach Waiting

	decisionPage := get("/t/" + runID + "/approve")
	decisionBody, _ := io.ReadAll(decisionPage.Body)
	_ = decisionPage.Body.Close()
	if decisionPage.StatusCode != http.StatusOK {
		t.Fatalf("GET decision page status = %d, body: %s", decisionPage.StatusCode, string(decisionBody))
	}
	if !strings.Contains(string(decisionBody), "disabled") {
		t.Error("decision fieldset does not render disabled against the real daemon")
	}

	req, _ := http.NewRequest(http.MethodPost, webBase+"/t/"+runID+"/approve/answer",
		strings.NewReader("decision=approve&reason=looks+fine&typedWord=approve&nonce=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range jar {
		req.AddCookie(c)
	}
	answerResp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST answer: %v", err)
	}
	answerBody, _ := io.ReadAll(answerResp.Body)
	_ = answerResp.Body.Close()
	if answerResp.StatusCode != http.StatusOK {
		t.Fatalf("answer status = %d, body: %s", answerResp.StatusCode, string(answerBody))
	}

	deadline = time.Now().Add(5 * time.Second)
	var finalStatus string
	for time.Now().Before(deadline) {
		finalStatus = h.runStatus(t, runID)
		if finalStatus == "succeeded" || finalStatus == "failed" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if finalStatus != "succeeded" {
		t.Fatalf("run status after web approval = %q, want succeeded", finalStatus)
	}
}

func milestoneApprovalDefPath(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("testdata/web_approval.yaml")
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	return abs
}
