package main_test

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// diffSourceRepoDir mirrors internal/engine's newSourceRepoDir: a real git
// repo living outside t.TempDir()'s own tree, since checkSafeSource
// (internal/workspace, L06) refuses a source repo nested under
// $KAIROS_HOME's own parent.
func diffSourceRepoDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "kairos-webui-diff-src-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func gitInit(t *testing.T, dir string) string {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "list.go"), []byte("package orders\n\nfunc List() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "initial")

	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// diffWebWorkflowPath is a two-node workspace: write workflow: n1 edits
// list.go (in scope), n2 edits list.go too but declares workspacePaths
// that do NOT cover it — a real, deliberately-provoked scope violation
// for the diff viewer's banner to actually have something to report.
func diffWebWorkflowPath(t *testing.T) string {
	t.Helper()
	const yaml = `
name: diff-web-e2e
nodes:
  - id: n1
    actor: shell
    workspace: write
    prompt: |
      printf 'package orders\n\nfunc List() int {\n\treturn 1\n}\n' > list.go
      echo '{"ok":true}' > "$KAIROS_OUTPUT_PATH"
    output: { ok: "bool!" }
  - id: n2
    actor: shell
    workspace: write
    workspacePaths: ["notes/**"]
    prompt: |
      printf 'package orders\n\nfunc List() int {\n\treturn 2\n}\n' > list.go
      echo '{"ok":true}' > "$KAIROS_OUTPUT_PATH"
    output: { ok: "bool!" }
`
	defPath := filepath.Join(t.TempDir(), "diff-web-e2e.yaml")
	if err := os.WriteFile(defPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}
	return defPath
}

// startWithEnv is daemonHarness.start plus extra environment variables —
// needed here for KAIROS_WORKSPACE_REPO/KAIROS_BASE_REF, which start's
// own fixed KAIROS_HOME-only env doesn't carry.
func (h *daemonHarness) startWithEnv(t *testing.T, timeout time.Duration, extraEnv ...string) {
	t.Helper()
	cmd := exec.Command(h.bin, "serve")
	cmd.Env = append(append(os.Environ(), "KAIROS_HOME="+h.home), extraEnv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting kairos serve: %v", err)
	}
	h.cmd = cmd
	pid := cmd.Process.Pid
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if h.pingOK() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("daemon did not become ready")
}

// TestWebUI_diffAndComparePagesAgainstARealDaemon is L20-webui.md's
// Future work, built here: the diff viewer and the compare page, both
// driven end to end against a real daemon, a real git workspace, and a
// real forked run — not a fake backend (internal/web's own diff_test.go/
// compare_test.go already cover the rendering logic in isolation; this
// proves the composition root wires Engine.Diff/Compare into the web
// pages for real).
func TestWebUI_diffAndComparePagesAgainstARealDaemon(t *testing.T) {
	bin := buildKairos(t)
	home := t.TempDir()
	t.Cleanup(func() { stopDaemon(t, home) })

	sourceRepo := diffSourceRepoDir(t)
	baseRef := gitInit(t, sourceRepo)

	h := newDaemonHarness(t, bin, home)
	h.startWithEnv(t, 5*time.Second, "KAIROS_WORKSPACE_REPO="+sourceRepo, "KAIROS_BASE_REF="+baseRef)
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
		r, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		return r
	}

	runID := h.createRun(t, diffWebWorkflowPath(t))
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

	// The whole-run diff viewer: real chroma-highlighted spans for a real
	// Go file, against the real base ref.
	diffResp := get("/runs/" + runID + "/diff")
	diffBody, _ := io.ReadAll(diffResp.Body)
	_ = diffResp.Body.Close()
	if diffResp.StatusCode != http.StatusOK {
		t.Fatalf("GET diff page status = %d, body: %s", diffResp.StatusCode, diffBody)
	}
	if !strings.Contains(string(diffBody), "list.go") {
		t.Errorf("diff page missing the changed file: %s", diffBody)
	}
	if !strings.Contains(string(diffBody), "chroma") {
		t.Errorf("diff page has no chroma-highlighted span: %s", diffBody)
	}

	// n2's own diff: a real, provoked scope violation (n2 wrote list.go
	// but declared workspacePaths: ["notes/**"]).
	n2Resp := get("/runs/" + runID + "/diff?node=n2")
	n2Body, _ := io.ReadAll(n2Resp.Body)
	_ = n2Resp.Body.Close()
	if n2Resp.StatusCode != http.StatusOK {
		t.Fatalf("GET n2 diff status = %d, body: %s", n2Resp.StatusCode, n2Body)
	}
	if !strings.Contains(string(n2Body), "scope-banner") {
		t.Errorf("n2's diff page missing the scope-violation banner: %s", n2Body)
	}

	// Fork at run.started's own sequence — early enough to guarantee no
	// workspace.snapshot.taken shares that exact sequence (forcing
	// --allow-drift's real live-snapshot path), late enough that the
	// copied prefix still carries the run's graph (domain.RunStarted),
	// so the forked run can actually dispatch instead of sitting Pending
	// forever with only a bare trigger.received copied.
	atSeq := 0
	for _, e := range h.streamEnvelopes(t, runID) {
		if e.EventType == "run.started" {
			atSeq = e.Sequence
			break
		}
	}
	if atSeq == 0 {
		t.Fatal("could not find run.started's sequence in the original run's event log")
	}
	forkOut := runKairos(t, bin, home, "-o", "json", "fork", runID, "--at", strconv.Itoa(atSeq), "--allow-drift")
	var forkResult struct {
		NewRunID string `json:"NewRunID"`
		Drifted  bool   `json:"Drifted"`
	}
	if err := json.Unmarshal([]byte(forkOut), &forkResult); err != nil {
		t.Fatalf("decoding fork output %q: %v", forkOut, err)
	}
	if !forkResult.Drifted {
		t.Fatalf("fork --at %d --allow-drift did not report Drifted — the test's own premise is broken", atSeq)
	}

	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && h.runStatus(t, forkResult.NewRunID) != "succeeded" {
		if s := h.runStatus(t, forkResult.NewRunID); s == "failed" {
			t.Fatalf("forked run failed")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if s := h.runStatus(t, forkResult.NewRunID); s != "succeeded" {
		for _, e := range h.streamEnvelopes(t, forkResult.NewRunID) {
			t.Logf("fork event: seq=%d type=%s", e.Sequence, e.EventType)
		}
		t.Fatalf("forked run status = %q, want succeeded", s)
	}

	compareResp := get("/compare?a=" + runID + "&b=" + forkResult.NewRunID)
	compareBody, _ := io.ReadAll(compareResp.Body)
	_ = compareResp.Body.Close()
	if compareResp.StatusCode != http.StatusOK {
		t.Fatalf("GET compare page status = %d, body: %s", compareResp.StatusCode, compareBody)
	}
	body := string(compareBody)
	if !strings.Contains(body, runID[:12]) || !strings.Contains(body, forkResult.NewRunID[:12]) {
		t.Errorf("compare page missing one of the two run ids: %s", body)
	}
	if !strings.Contains(body, "forked from") {
		t.Errorf("compare page missing the fork relationship: %s", body)
	}
	if !strings.Contains(body, "restored from a later, live one instead") {
		t.Errorf("compare page missing the real drift detail: %s", body)
	}
	if mismatches := h.dbVerify(t); len(mismatches) != 0 {
		t.Errorf("db verify found mismatches: %v", mismatches)
	}
}
