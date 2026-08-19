package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// buildKairos builds the real binary once per test and returns its path —
// an integration test, not application code (see TestVersionCommand's
// comment: exec.Command here does not violate
// TestArchitecture_noExecOutsideExecutor, which only scans non-test
// files).
func buildKairos(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "kairos")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building kairos: %v\n%s", err, out)
	}
	return bin
}

func fixIssuePath(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("../../internal/registry/testdata/fix-issue.yaml")
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("fixture missing: %v", err)
	}
	return abs
}

// runKairos runs bin with args against home as $KAIROS_HOME, auto-starting
// a daemon if none is running (kairos <verb> ... starts a daemon if none
// is running — 01-architecture.md), and returns stdout.
func runKairos(t *testing.T, bin, home string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "KAIROS_HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("kairos %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// stopDaemon reads the PID daemon.lock recorded and kills it, cleaning up
// the background daemon an auto-start left running past the test.
func stopDaemon(t *testing.T, home string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, "daemon.lock"))
	if err != nil {
		return
	}
	pidStr := strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0])
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = proc.Kill()
}

// TestIntegration_runShowListVerify exercises the demo path end to end:
// `kairos run` (auto-starting the daemon), `kairos show`, `kairos ls`, and
// `kairos db verify`, all against a real daemon over a real unix socket.
func TestIntegration_runShowListVerify(t *testing.T) {
	bin := buildKairos(t)
	home := t.TempDir()
	t.Cleanup(func() { stopDaemon(t, home) })

	runOut := runKairos(t, bin, home, "-o", "json", "run", fixIssuePath(t))
	var created struct {
		RunID  string `json:"runId"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(runOut), &created); err != nil {
		t.Fatalf("decoding run output %q: %v", runOut, err)
	}
	if created.RunID == "" {
		t.Fatal("expected a non-empty runId")
	}
	if created.Status != "running" {
		t.Errorf("Status = %q, want running", created.Status)
	}

	showOut := runKairos(t, bin, home, "-o", "json", "show", created.RunID)
	var state struct {
		ID     string `json:"ID"`
		Status string `json:"Status"`
	}
	if err := json.Unmarshal([]byte(showOut), &state); err != nil {
		t.Fatalf("decoding show output %q: %v", showOut, err)
	}
	if state.ID != created.RunID {
		t.Errorf("show ID = %q, want %q", state.ID, created.RunID)
	}

	lsOut := runKairos(t, bin, home, "-o", "json", "ls")
	var runs []struct {
		RunID string `json:"RunID"`
	}
	if err := json.Unmarshal([]byte(lsOut), &runs); err != nil {
		t.Fatalf("decoding ls output %q: %v", lsOut, err)
	}
	found := false
	for _, r := range runs {
		if r.RunID == created.RunID {
			found = true
		}
	}
	if !found {
		t.Errorf("kairos ls did not list %s; output: %s", created.RunID, lsOut)
	}

	verifyOut := runKairos(t, bin, home, "-o", "json", "db", "verify")
	var report struct {
		MismatchedRunIDs []string `json:"mismatchedRunIds"`
	}
	if err := json.Unmarshal([]byte(verifyOut), &report); err != nil {
		t.Fatalf("decoding verify output %q: %v", verifyOut, err)
	}
	if len(report.MismatchedRunIDs) != 0 {
		t.Errorf("db verify found mismatches: %v", report.MismatchedRunIDs)
	}
}

// TestIntegration_daemonSurvivesASecondAutoStartAttempt proves the
// PID-file lock (decision #2) refuses a second daemon rather than
// silently running two.
func TestIntegration_daemonSurvivesASecondAutoStartAttempt(t *testing.T) {
	bin := buildKairos(t)
	home := t.TempDir()
	t.Cleanup(func() { stopDaemon(t, home) })

	// First run auto-starts a daemon.
	_ = runKairos(t, bin, home, "status")
	time.Sleep(200 * time.Millisecond)

	cmd := exec.Command(bin, "serve")
	cmd.Env = append(os.Environ(), "KAIROS_HOME="+home)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected a second `kairos serve` to fail (daemon already running), got clean exit; output: %s", out)
	}
	if !strings.Contains(string(out), "already running") {
		t.Errorf("output = %q, want it to mention the daemon is already running", out)
	}
}
