package main_test

import (
	"encoding/json"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestEngine_ctrlCInterruptsThenResumes is the milestone's companion
// scenario to TestEngine_survivesKillMidRun: SIGINT (not SIGKILL) hits the
// daemon's whole process group, which is the graceful path — the daemon
// must interrupt the executing node cleanly and exit 0 within a few
// seconds, WITHOUT ever needing a restart's reconciliation/orphan-reap
// machinery, because the graceful shutdown itself kills the child before
// exiting.
func TestEngine_ctrlCInterruptsThenResumes(t *testing.T) {
	bin := buildKairos(t)
	home := t.TempDir()

	h := newDaemonHarness(t, bin, home)
	h.start(t, 5*time.Second)
	h.waitForReconciled(t, 3*time.Second)

	runID := h.createRun(t, milestoneDefPath(t))

	pidFile := filepath.Join(home, "work", runID, "n2.pid")
	n2PID := readPIDFile(t, pidFile, 5*time.Second)

	// SIGINT the whole process group (Ctrl-C semantics) — the daemon owns
	// its own group (Setpgid: true), so this only reaches the daemon
	// itself, matching how a real terminal Ctrl-C behaves.
	if err := syscall.Kill(-h.cmd.Process.Pid, syscall.SIGINT); err != nil {
		t.Fatalf("SIGINT process group: %v", err)
	}

	done := make(chan *syscall.WaitStatus, 1)
	go func() {
		state, err := h.cmd.Process.Wait()
		if err != nil {
			done <- nil
			return
		}
		ws := state.Sys().(syscall.WaitStatus)
		done <- &ws
	}()

	select {
	case ws := <-done:
		if ws == nil {
			t.Fatal("Wait() failed")
		}
		if ws.Exited() && ws.ExitStatus() != 0 {
			t.Errorf("daemon exit code = %d, want 0", ws.ExitStatus())
		}
	case <-time.After(15 * time.Second): // Cancel blocks the full killGrace (10s in production) even when the child already died from SIGTERM
		t.Fatal("daemon did not exit within 15s of SIGINT")
	}

	// The child must be dead too — a graceful shutdown's whole point is
	// that it doesn't leave orphans behind for the next boot to clean up.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && processAlive(n2PID) {
		time.Sleep(20 * time.Millisecond)
	}
	if processAlive(n2PID) {
		t.Error("expected n2's child process to be dead after graceful shutdown")
	}

	// node.execution.interrupted must be recorded for n2 — WITHOUT a
	// restart ever having happened.
	h2 := newDaemonHarness(t, bin, home)
	h2.start(t, 5*time.Second)
	h2.waitForReconciled(t, 3*time.Second)

	runEnvs := h2.streamEnvelopes(t, runID)
	var sawInterrupted, sawOrphanReapedForRun bool
	for _, e := range runEnvs {
		if e.EventType == "node.execution.interrupted" {
			var payload struct{ NodeID string }
			_ = json.Unmarshal(e.Event, &payload)
			if payload.NodeID == "n2" {
				sawInterrupted = true
			}
		}
	}
	if !sawInterrupted {
		t.Error("expected node.execution.interrupted{n2} to be recorded by the graceful shutdown")
	}

	sysEnvs := h2.streamEnvelopes(t, "system")
	for _, e := range sysEnvs {
		if e.EventType == "process.orphan.reaped" {
			sawOrphanReapedForRun = true
		}
	}
	if sawOrphanReapedForRun {
		t.Error("expected no process.orphan.reaped on restart — a clean shutdown should leave nothing to reap")
	}

	_ = h2.cmd.Process.Signal(syscall.SIGTERM)
	_, _ = h2.cmd.Process.Wait()
}
