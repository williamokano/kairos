package main_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
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

// daemonHarness drives a real `kairos serve` subprocess against a private
// $KAIROS_HOME, giving the milestone tests raw access to the daemon's
// socket for SSE and API calls the CLI alone doesn't expose conveniently.
type daemonHarness struct {
	t        *testing.T
	bin      string
	home     string
	sockPath string
	cmd      *exec.Cmd
}

func newDaemonHarness(t *testing.T, bin, home string) *daemonHarness {
	return &daemonHarness{t: t, bin: bin, home: home, sockPath: filepath.Join(home, "daemon.sock")}
}

// start launches `kairos serve` in its own process group (so the test can
// SIGKILL or SIGINT it without affecting the test process itself) and
// waits for the socket to answer. The daemon does not bind its socket
// until engine.Reconcile has returned (cmd/kairos/serve.go's boot order),
// so a successful ping already proves reconciliation happened — the
// generous timeout exists because a restart's reconciliation may have to
// wait out a full TERM->killGrace->KILL cycle on an orphaned process.
func (h *daemonHarness) start(t *testing.T, timeout time.Duration) {
	t.Helper()
	cmd := exec.Command(h.bin, "serve")
	cmd.Env = append(os.Environ(), "KAIROS_HOME="+h.home)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting kairos serve: %v", err)
	}
	h.cmd = cmd

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if h.pingOK() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("daemon did not become ready")
}

func (h *daemonHarness) pingOK() bool {
	client := h.httpClient()
	resp, err := client.Get("http://kairos/status")
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == 200
}

func (h *daemonHarness) httpClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", h.sockPath)
			},
		},
		Timeout: 10 * time.Second,
	}
}

// waitForReconciled polls the system stream (via /events) for
// engine.reconciled, reading it BEFORE trusting readiness — matching the
// milestone's exact ordering requirement ("read the event first, then
// assert readiness, in that order").
func (h *daemonHarness) waitForReconciled(t *testing.T, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if h.systemStreamHasEventType(t, "engine.reconciled") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("engine.reconciled did not appear within the timeout")
}

func (h *daemonHarness) systemStreamHasEventType(t *testing.T, eventType string) bool {
	t.Helper()
	client := h.httpClient()
	resp, err := client.Get("http://kairos/events?stream=system&after=0")
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()

	found := false
	scanner := bufio.NewScanner(resp.Body)
	deadline := time.Now().Add(500 * time.Millisecond)
	for scanner.Scan() {
		if time.Now().After(deadline) {
			break
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var env struct {
			EventType string `json:"EventType"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &env); err == nil {
			if env.EventType == eventType {
				found = true
				break
			}
		}
	}
	return found
}

// runEnvelope mirrors the fields the tests need from an events.Envelope's
// JSON encoding.
type runEnvelope struct {
	Sequence  int             `json:"Sequence"`
	GlobalSeq int64           `json:"GlobalSeq"`
	EventType string          `json:"EventType"`
	Event     json.RawMessage `json:"Event"`
}

// streamEnvelopes fetches every envelope currently in streamID via the
// non-streaming historical portion of /events (reads until the server has
// no more buffered history to send within the grace window).
func (h *daemonHarness) streamEnvelopes(t *testing.T, streamID string) []runEnvelope {
	t.Helper()
	client := h.httpClient()
	resp, err := client.Get("http://kairos/events?stream=" + streamID + "&after=0")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var envs []runEnvelope
	scanner := bufio.NewScanner(resp.Body)
	deadline := time.Now().Add(1 * time.Second)
	for scanner.Scan() {
		if time.Now().After(deadline) {
			break
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var env runEnvelope
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &env); err == nil {
			envs = append(envs, env)
		}
	}
	return envs
}

func (h *daemonHarness) createRun(t *testing.T, definitionPath string) string {
	t.Helper()
	client := h.httpClient()
	body, _ := json.Marshal(map[string]any{"definitionPath": definitionPath})
	resp, err := client.Post("http://kairos/runs", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("POST /runs: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		RunID string `json:"runId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding /runs response: %v", err)
	}
	if resp.StatusCode != 201 {
		t.Fatalf("POST /runs: status %d", resp.StatusCode)
	}
	return out.RunID
}

func (h *daemonHarness) runStatus(t *testing.T, runID string) string {
	t.Helper()
	client := h.httpClient()
	resp, err := client.Get("http://kairos/runs/" + runID)
	if err != nil {
		t.Fatalf("GET /runs/%s: %v", runID, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Status string `json:"Status"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out.Status
}

func (h *daemonHarness) dbVerify(t *testing.T) []string {
	t.Helper()
	client := h.httpClient()
	resp, err := client.Post("http://kairos/db/verify", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /db/verify: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		MismatchedRunIDs []string `json:"mismatchedRunIds"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out.MismatchedRunIDs
}

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func readPIDFile(t *testing.T, path string, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil && len(strings.TrimSpace(string(b))) > 0 {
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err == nil {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for pid file %s", path)
	return 0
}

func milestoneDefPath(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("testdata/milestone.yaml")
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	return abs
}

// TestEngine_survivesKillMidRun follows 12-build-plan.md's exact
// milestone procedure: SIGKILL the daemon mid-node, restart, and assert
// by EVENT (not outcome) that the orphaned child is reaped, the node is
// recorded Lost then retried, and the run completes — with db verify
// clean throughout.
func TestEngine_survivesKillMidRun(t *testing.T) {
	bin := buildKairos(t)
	home := t.TempDir()

	h := newDaemonHarness(t, bin, home)
	h.start(t, 5*time.Second)
	h.waitForReconciled(t, 3*time.Second)

	runID := h.createRun(t, milestoneDefPath(t))

	pidFile := filepath.Join(home, "work", runID, "n2.pid")
	n2PID := readPIDFile(t, pidFile, 5*time.Second)

	// Step 3: SIGKILL the daemon.
	if err := h.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL: %v", err)
	}
	state, err := h.cmd.Process.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !state.Sys().(syscall.WaitStatus).Signaled() {
		t.Fatalf("wait status = %v, want signaled", state.Sys())
	}

	// Step 4: the mess exists — n2's child is still alive.
	if !processAlive(n2PID) {
		t.Fatal("n2's child process is already dead — the process-group detachment isn't holding; the test proves nothing")
	}

	// Step 5: restart. Readiness must not be trusted until
	// engine.reconciled is read from the log.
	h2 := newDaemonHarness(t, bin, home)
	h2.start(t, 20*time.Second) // may include a full TERM->killGrace->KILL cycle on the orphan
	h2.waitForReconciled(t, 5*time.Second)

	// Step 6: assert by event.
	deadline := time.Now().Add(5 * time.Second)
	var sawOrphanReaped, sawLostAttempt1, sawStartedAttempt2 bool
	for time.Now().Before(deadline) && (!sawOrphanReaped || !sawLostAttempt1 || !sawStartedAttempt2) {
		sysEnvs := h2.streamEnvelopes(t, "system")
		for _, e := range sysEnvs {
			if e.EventType == "process.orphan.reaped" {
				sawOrphanReaped = true
			}
		}
		runEnvs := h2.streamEnvelopes(t, runID)
		for _, e := range runEnvs {
			switch e.EventType {
			case "node.execution.lost":
				var payload struct{ NodeID string }
				_ = json.Unmarshal(e.Event, &payload)
				if payload.NodeID == "n2" {
					sawLostAttempt1 = true
				}
			case "node.execution.started":
				var payload struct {
					NodeID  string
					Attempt int
				}
				_ = json.Unmarshal(e.Event, &payload)
				if payload.NodeID == "n2" && payload.Attempt == 2 {
					sawStartedAttempt2 = true
				}
			}
		}
		if !sawOrphanReaped || !sawLostAttempt1 || !sawStartedAttempt2 {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if !sawOrphanReaped {
		t.Error("expected process.orphan.reaped on the system stream")
	}
	if !sawLostAttempt1 {
		t.Error("expected node.execution.lost{n2} in the run's stream")
	}
	if !sawStartedAttempt2 {
		t.Error("expected node.execution.started{n2, attempt:2} in the run's stream")
	}
	if processAlive(n2PID) {
		t.Error("expected the orphaned n2 process to be dead after reconciliation")
	}

	// Step 7: poll to terminal.
	deadline = time.Now().Add(40 * time.Second) // n2's retried attempt sleeps 30s
	var finalStatus string
	for time.Now().Before(deadline) {
		finalStatus = h2.runStatus(t, runID)
		if finalStatus == "succeeded" || finalStatus == "failed" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if finalStatus != "succeeded" {
		t.Fatalf("final run status = %q, want succeeded", finalStatus)
	}

	// Step 8 (partial): sequences are gapless 1..N, no duplicates.
	envs := h2.streamEnvelopes(t, runID)
	seen := map[int]bool{}
	for _, e := range envs {
		if seen[e.Sequence] {
			t.Errorf("duplicate sequence %d in run stream", e.Sequence)
		}
		seen[e.Sequence] = true
	}
	for i := 1; i <= len(envs); i++ {
		if !seen[i] {
			t.Errorf("gap at sequence %d in run stream (len=%d)", i, len(envs))
		}
	}

	// Step 9: evidence of the killed attempt is retained; n3 ran exactly
	// once (the ledger idempotency guard proves it, and the milestone's
	// n3 only ever gets ONE attempt in this scenario since the crash hit
	// n2, not n3).
	attempt1Dir := filepath.Join(home, "work", runID, "n2#a1.i1")
	if _, err := os.Stat(attempt1Dir); err != nil {
		t.Errorf("expected the killed attempt's directory to still exist: %v", err)
	}
	attempt2Dir := filepath.Join(home, "work", runID, "n2#a2.i1")
	if _, err := os.Stat(attempt2Dir); err != nil {
		t.Errorf("expected the retried attempt's directory to exist: %v", err)
	}
	ledger, err := os.ReadFile(filepath.Join(home, "work", runID, "ledger.txt"))
	if err != nil {
		t.Fatalf("reading ledger: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(ledger)), "\n")
	if len(lines) != 1 || lines[0] != "n3-done" {
		t.Errorf("ledger = %q, want exactly one line \"n3-done\"", string(ledger))
	}

	// Step 10: replay matches projection.
	if mismatches := h2.dbVerify(t); len(mismatches) != 0 {
		t.Errorf("db verify found mismatches: %v", mismatches)
	}

	_ = h2.cmd.Process.Signal(syscall.SIGTERM)
	_, _ = h2.cmd.Process.Wait()
}
