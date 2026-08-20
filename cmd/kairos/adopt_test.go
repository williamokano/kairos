package main_test

import (
	"encoding/json"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestEngine_adoptSurvivesRestartWithoutKillingTheChild proves
// RestartPolicy: adopt's whole point against the real binary: a daemon
// crash mid-node must NOT kill or retry a node that opted into adoption —
// the same process the pre-crash daemon started keeps running,
// unsupervised, until it finishes naturally, and the restarted daemon
// simply picks its eventual output back up.
func TestEngine_adoptSurvivesRestartWithoutKillingTheChild(t *testing.T) {
	bin := buildKairos(t)
	home := t.TempDir()

	h := newDaemonHarness(t, bin, home)
	h.start(t, 5*time.Second)
	h.waitForReconciled(t, 3*time.Second)

	adoptDefPath, err := filepath.Abs("testdata/adopt.yaml")
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	runID := h.createRun(t, adoptDefPath)

	pidFile := filepath.Join(home, "work", runID, "n1.pid")
	n1PID := readPIDFile(t, pidFile, 5*time.Second)

	if err := h.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL: %v", err)
	}
	if _, err := h.cmd.Process.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !processAlive(n1PID) {
		t.Fatal("n1's child process is already dead — the test proves nothing")
	}

	h2 := newDaemonHarness(t, bin, home)
	h2.start(t, 20*time.Second)
	h2.waitForReconciled(t, 5*time.Second)

	// The adopted process must never have been signalled: it should
	// still be the exact same pid, alive, uninterrupted, right after
	// reconciliation completes.
	if !processAlive(n1PID) {
		t.Fatal("expected the adopted process to survive reconciliation, but it is dead")
	}

	sysEnvs := h2.streamEnvelopes(t, "system")
	for _, e := range sysEnvs {
		if e.EventType == "process.orphan.reaped" {
			t.Error("expected no process.orphan.reaped for an adopted node")
		}
	}

	runEnvs := h2.streamEnvelopes(t, runID)
	var startedCount int
	for _, e := range runEnvs {
		switch e.EventType {
		case "node.execution.started":
			startedCount++
		case "node.execution.lost":
			t.Error("expected no node.execution.lost for an adopted node")
		}
	}
	if startedCount != 1 {
		t.Errorf("node.execution.started count = %d, want 1 — adoption must not dispatch a fresh attempt", startedCount)
	}

	deadline := time.Now().Add(15 * time.Second) // n1's remaining sleep, plus margin
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

	runEnvs = h2.streamEnvelopes(t, runID)
	var sawOutput bool
	for _, e := range runEnvs {
		if e.EventType == "node.output.received" {
			var payload struct {
				NodeID string
				Output json.RawMessage
			}
			_ = json.Unmarshal(e.Event, &payload)
			if payload.NodeID == "n1" {
				sawOutput = true
			}
		}
	}
	if !sawOutput {
		t.Error("expected node.output.received{n1} — the adopted process's real exit must be what finished the run")
	}

	if mismatches := h2.dbVerify(t); len(mismatches) != 0 {
		t.Errorf("db verify found mismatches: %v", mismatches)
	}

	_ = h2.cmd.Process.Signal(syscall.SIGTERM)
	_, _ = h2.cmd.Process.Wait()
}
