package main_test

import (
	"bytes"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestKairos_bareNonTTYPrintsStatusAndExits proves the load-bearing half
// of L15's bare-`kairos` change that's practical to automate: piped
// (non-TTY) stdout must never attach the full-screen TUI — "kairos | grep
// must never hang on a full-screen app" (09-cli-and-tui.md). The
// TTY-attaches branch itself needs a real pseudo-terminal to exercise
// automatically and isn't covered here — see L15-tui.md's Tests section
// for why that's an honest, named gap rather than a silently-skipped case.
func TestKairos_bareNonTTYPrintsStatusAndExits(t *testing.T) {
	bin := buildKairos(t)
	home := t.TempDir()

	h := newDaemonHarness(t, bin, home)
	h.start(t, 5*time.Second)
	h.waitForReconciled(t, 3*time.Second)

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "KAIROS_HOME="+home)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout // exec.Cmd.Stdout as a bytes.Buffer is never a *os.File, so isTTY sees a non-TTY here, exactly like a real pipe.
	cmd.Stderr = &stderr

	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting bare kairos: %v", err)
	}
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("bare kairos (non-TTY) exited non-zero: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("bare kairos with piped stdout did not exit within 5s — it attached the TUI instead of printing status")
	}

	if stdout.Len() == 0 {
		t.Fatalf("expected a status line on stdout, got nothing (stderr=%s)", stderr.String())
	}

	_ = h.cmd.Process.Kill()
	_, _ = h.cmd.Process.Wait()
}
