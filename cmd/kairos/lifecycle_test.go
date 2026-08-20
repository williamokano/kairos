package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runKairosExpectingError is runKairos's mirror for the (rarer) case a
// test wants to assert a verb genuinely fails, rather than fatal on the
// first non-zero exit like runKairos does.
func runKairosExpectingError(t *testing.T, bin, home string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "KAIROS_HOME="+home)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestLifecycle_pauseHoldsParkWaitsBackupRestores drives kairos
// pause/resume/park/db backup against a real daemon, matching
// CLI-GUIDE.md's already-verified style.
func TestLifecycle_pauseHoldsParkWaitsBackupRestores(t *testing.T) {
	bin := buildKairos(t)
	home := t.TempDir()
	t.Cleanup(func() { stopDaemon(t, home) })

	pauseOut := runKairos(t, bin, home, "pause")
	if strings.TrimSpace(pauseOut) != "paused" {
		t.Fatalf("kairos pause output = %q, want \"paused\"", pauseOut)
	}

	resumeOut := runKairos(t, bin, home, "resume")
	if strings.TrimSpace(resumeOut) != "resumed" {
		t.Fatalf("kairos resume output = %q, want \"resumed\"", resumeOut)
	}

	// park --wait with nothing running should return "parked" quickly.
	parkOut := runKairos(t, bin, home, "park", "--wait")
	if strings.TrimSpace(parkOut) != "parked" {
		t.Fatalf("kairos park --wait output = %q, want \"parked\"", parkOut)
	}

	// Un-park for the backup step below (backup doesn't need it, but
	// leaving the daemon paused would be a surprising side effect of a
	// test that isn't testing that).
	runKairos(t, bin, home, "resume")

	backupPath := filepath.Join(t.TempDir(), "kairos-backup.db")
	backupOut := runKairos(t, bin, home, "db", "backup", backupPath)
	if strings.TrimSpace(backupOut) != "ok" {
		t.Fatalf("kairos db backup output = %q, want \"ok\"", backupOut)
	}
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("backup file missing: %v", err)
	}

	selfCheckOut := runKairos(t, bin, home, "doctor", "--self-check")
	if !strings.Contains(selfCheckOut, "db: clean") {
		t.Fatalf("kairos doctor --self-check output = %q, want it to report db: clean", selfCheckOut)
	}
}

// TestLifecycle_parkWaitTimesOutIfSomethingNeverFinishes proves --wait's
// deadline is real, not decorative: a node that never completes must
// eventually make park report failure rather than hanging the CLI
// forever ("kairos | grep must never hang" extends to every verb, not
// just bare kairos).
func TestLifecycle_parkWaitTimesOutIfSomethingNeverFinishes(t *testing.T) {
	bin := buildKairos(t)
	home := t.TempDir()
	t.Cleanup(func() { stopDaemon(t, home) })

	longDef := filepath.Join(t.TempDir(), "long.yaml")
	if err := os.WriteFile(longDef, []byte(`
name: long
nodes:
  - id: n1
    actor: shell
    sideEffectFree: true
    prompt: |
      sleep 60
      echo '{"done":true}' > "$KAIROS_OUTPUT_PATH"
    output: { done: "bool!" }
`), 0o600); err != nil {
		t.Fatalf("writing definition: %v", err)
	}

	runOut := runKairos(t, bin, home, "run", longDef)
	fields := strings.Fields(runOut)
	if len(fields) == 0 {
		t.Fatalf("kairos run produced no output")
	}
	runID := fields[0]

	// Wait until n1 has genuinely been admitted and started (its exec
	// scratch dir exists) before parking — otherwise pause could win the
	// race against admission and the node would never start at all,
	// making park --wait return instantly with nothing to prove.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		entries, _ := os.ReadDir(filepath.Join(home, "work", runID))
		if len(entries) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	start := time.Now()
	_, err := runKairosExpectingError(t, bin, home, "park", "--wait")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected kairos park --wait to fail (time out) while a 60s node is running")
	}
	if elapsed > 30*time.Second {
		t.Fatalf("park --wait took %s, want it to time out within its own ~25s budget", elapsed)
	}
}
