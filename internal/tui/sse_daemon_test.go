package tui

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/williamokano/kairos/internal/cli"
)

// buildKairosForSSETest builds the real binary once for this test file — a
// test-only exec.Command, which is why it does not violate
// TestArchitecture_tuiHasNoExecution (that check loads the non-test build
// of internal/tui's packages and never sees _test.go files at all; the
// same posture cmd/kairos's own integration tests already document).
func buildKairosForSSETest(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "kairos")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/williamokano/kairos/cmd/kairos")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building kairos: %v\n%s", err, out)
	}
	return bin
}

func sseTestHTTPClient(sockPath string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sockPath)
			},
		},
		Timeout: 10 * time.Second,
	}
}

// startRealDaemon boots a real `kairos serve` against home, waiting for the
// socket to answer AND for engine.reconciled to appear on the system
// stream before returning — mirroring cmd/kairos's own daemonHarness
// (kill_mid_run_test.go), duplicated narrowly here rather than shared
// across packages for one helper.
func startRealDaemon(t *testing.T, bin, home string, extraEnv ...string) (sockPath string, cleanup func()) {
	t.Helper()
	sockPath = filepath.Join(home, "daemon.sock")

	cmd := exec.Command(bin, "serve")
	// KAIROS_WEB_ADDR=...:0 binds the web UI to an ephemeral loopback
	// port instead of the fixed 127.0.0.1:7717 (internal/config.Load's
	// default). A real bug this test file found: cmd/kairos's own daemon-
	// spawning tests bind the fixed port too, and since `go test ./...`
	// runs different packages' test binaries concurrently, two real
	// `kairos serve` processes racing for the same fixed port collide
	// ("address already in use") — latent until this package also started
	// booting a real daemon. This test doesn't use the web UI at all, so
	// an ephemeral port sidesteps the collision entirely.
	cmd.Env = append(append(os.Environ(), "KAIROS_HOME="+home, "KAIROS_WEB_ADDR=127.0.0.1:0"), extraEnv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting kairos serve: %v", err)
	}
	pid := cmd.Process.Pid
	cleanup = func() { _ = syscall.Kill(-pid, syscall.SIGKILL) }

	httpClient := sseTestHTTPClient(sockPath)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := httpClient.Get("http://kairos/status")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if systemStreamReconciled(httpClient) {
			return sockPath, cleanup
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("daemon did not become ready (no engine.reconciled)")
	return "", cleanup
}

func systemStreamReconciled(httpClient *http.Client) bool {
	resp, err := httpClient.Get("http://kairos/events?stream=system&after=0")
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()

	found := false
	scanner := bufio.NewScanner(resp.Body)
	deadline := time.Now().Add(300 * time.Millisecond)
	for scanner.Scan() {
		if time.Now().After(deadline) {
			break
		}
		if strings.Contains(scanner.Text(), "engine.reconciled") {
			found = true
			break
		}
	}
	return found
}

// TestSSESubscription_pushesRunEventsAsTheyLandNotOnATimer is the
// end-to-end proof the stage asked for: against a REAL daemon and a REAL
// in-flight node (n2 sleeps ~1s), internal/tui's actual sseSubscription
// (the same type Model.Init wires up) receives that run's events one at a
// time, as they happen, via the production waitForSSE/FollowEvents path —
// not gathered once at the end, and not on any polling cadence. If this
// package still polled on a 2s tea.Tick, the run (~1.1s total) could
// finish before the first tick ever fired; here the first event for the
// run is observed well under that, and node n2's started/succeeded pair
// are observed with a real time gap matching its actual sleep, proving
// incremental delivery rather than a single batched fetch.
func TestSSESubscription_pushesRunEventsAsTheyLandNotOnATimer(t *testing.T) {
	bin := buildKairosForSSETest(t)
	home := t.TempDir()
	sockPath, cleanup := startRealDaemon(t, bin, home)
	defer cleanup()

	client := cli.NewClient(sockPath)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub := newSSESubscription(ctx, client)

	// Drain the pre-existing backlog (system-stream boot events) so the
	// run-scoped assertions below only see messages for OUR run.
	msgCh := make(chan tea.Msg, 4096)
	go func() {
		for {
			msg := waitForSSE(sub.ch)()
			select {
			case msgCh <- msg:
			case <-ctx.Done():
				return
			}
		}
	}()

	defPath, err := filepath.Abs("testdata/live.yaml")
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}

	t0 := time.Now()
	created, err := client.CreateRun(context.Background(), defPath, nil, "")
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	runID := created.RunID

	type timedEvent struct {
		env cli.Envelope
		at  time.Time
	}
	var mu sync.Mutex
	var events []timedEvent
	var firstSeen time.Time

	deadline := time.After(15 * time.Second)
collect:
	for {
		select {
		case msg := <-msgCh:
			ev, ok := msg.(sseEventMsg)
			if !ok || ev.env.StreamID != runID {
				continue
			}
			now := time.Now()
			mu.Lock()
			if firstSeen.IsZero() {
				firstSeen = now
			}
			events = append(events, timedEvent{env: ev.env, at: now})
			mu.Unlock()
			if ev.env.EventType == "node.output.received" {
				var payload struct{ NodeID string }
				_ = json.Unmarshal(ev.env.Event, &payload)
				if payload.NodeID == "n3" {
					break collect
				}
			}
		case <-deadline:
			break collect
		}
	}

	mu.Lock()
	defer mu.Unlock()

	if firstSeen.IsZero() {
		t.Fatal("never received a single SSE-pushed event for the new run")
	}
	if d := firstSeen.Sub(t0); d > 2*time.Second {
		t.Errorf("first push for the new run arrived %v after creation — too slow to be a real push (old poll cadence was 2s)", d)
	}

	var n2Started, n2Succeeded time.Time
	for _, e := range events {
		if e.env.EventType != "node.execution.started" && e.env.EventType != "node.output.received" {
			continue
		}
		var payload struct{ NodeID string }
		_ = json.Unmarshal(e.env.Event, &payload)
		if payload.NodeID != "n2" {
			continue
		}
		switch e.env.EventType {
		case "node.execution.started":
			n2Started = e.at
		case "node.output.received":
			n2Succeeded = e.at
		}
	}
	if n2Started.IsZero() || n2Succeeded.IsZero() {
		t.Fatalf("did not observe both node.execution.started and node.output.received pushed for n2 (got %d run events total)", len(events))
	}
	if gap := n2Succeeded.Sub(n2Started); gap < 700*time.Millisecond {
		t.Errorf("n2 started->succeeded push gap = %v, want >= ~700ms (n2 sleeps 1s) — a batched delivery at the end would show a near-zero gap", gap)
	}

	if len(events) < 3 {
		t.Errorf("expected at least 3 distinct pushed events (n1, n2, n3 lifecycle), got %d", len(events))
	}
}
