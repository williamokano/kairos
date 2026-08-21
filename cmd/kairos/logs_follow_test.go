package main_test

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func logsFollowDefPath(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("testdata/logs-follow.yaml")
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	return abs
}

// TestIntegration_logsFollowStreamsAsProduced is the real end-to-end proof
// for `kairos logs --follow` (L04-daemon-api-cli.md's deferred verb): run
// it against a REAL daemon with a REAL in-flight node (n2 sleeps ~1s
// between n1 and n3), and prove the CLI process prints n2's
// node.execution.started line and then its node.output.received line with
// a real time gap between them — i.e. it is genuinely tailing the SSE
// stream as output is produced, not just dumping everything once the run
// (and the process) is already finished.
func TestIntegration_logsFollowStreamsAsProduced(t *testing.T) {
	bin := buildKairos(t)
	home := t.TempDir()

	h := newDaemonHarness(t, bin, home)
	h.start(t, 5*time.Second)
	h.waitForReconciled(t, 3*time.Second)

	runID := h.createRun(t, logsFollowDefPath(t))

	cmd := exec.Command(bin, "logs", runID, "--follow")
	cmd.Env = append(os.Environ(), "KAIROS_HOME="+home)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting kairos logs --follow: %v", err)
	}

	type timedLine struct {
		text string
		at   time.Time
	}
	lines := make(chan timedLine, 4096)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- timedLine{text: scanner.Text(), at: time.Now()}
		}
		close(lines)
	}()

	var n2Started, n2Output time.Time
	deadline := time.After(15 * time.Second)
collect:
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				break collect
			}
			if strings.Contains(l.text, "node.execution.started") && strings.Contains(l.text, "\"n2\"") {
				if n2Started.IsZero() {
					n2Started = l.at
				}
			}
			if strings.Contains(l.text, "node.output.received") && strings.Contains(l.text, "\"n2\"") {
				if n2Output.IsZero() {
					n2Output = l.at
					break collect
				}
			}
		case <-deadline:
			break collect
		}
	}

	// End the follow cleanly (SIGINT, exactly like a user's Ctrl-C) rather
	// than leaving it running or killing it — proving the process really
	// is a long-lived tail, not something that already exited on its own.
	_ = cmd.Process.Signal(syscall.SIGINT)
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	select {
	case err := <-waitErr:
		if err != nil {
			t.Errorf("kairos logs --follow after SIGINT: %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Error("kairos logs --follow did not exit within 5s of SIGINT")
	}

	if n2Started.IsZero() {
		t.Fatal("never saw n2's node.execution.started line from kairos logs --follow")
	}
	if n2Output.IsZero() {
		t.Fatal("never saw n2's node.output.received line from kairos logs --follow")
	}
	if gap := n2Output.Sub(n2Started); gap < 700*time.Millisecond {
		t.Errorf("n2 started->output line gap = %v, want >= ~700ms (n2 sleeps 1s) — a batched dump at the end would show a near-zero gap", gap)
	}
}
