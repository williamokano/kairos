package local_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/executor/local"
)

func TestCancel_killsAProcessThatIgnoresSIGTERM(t *testing.T) {
	exec := local.New(local.DefaultBootIDProvider())
	dir := t.TempDir()
	readyFile := filepath.Join(dir, "ready")
	// trap SIGTERM and ignore it, forcing Cancel to fall through to SIGKILL.
	// Signalling readiness via a file (rather than a fixed sleep) avoids a
	// race against the shell installing the trap before SIGTERM arrives.
	// The blocking step is a builtin busy-loop (`true`), not `sleep`: a
	// forked `sleep` grandchild would outlive cmd.Wait()'s reap of its
	// direct child (sh) as a zombie in the same process group, making
	// ProcessGroupAlive's kill(pgid, 0) probe flaky immediately after
	// SIGKILL — a real reaping subtlety, not a bug in Cancel itself.
	started, err := exec.Start(context.Background(), local.ExecSpec{
		Dir:  dir,
		Argv: []string{"sh", "-c", "trap '' TERM; touch " + readyFile + "; while true; do true; done"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitForFile(t, readyFile, 2*time.Second)

	done := make(chan struct{})
	go func() {
		_, _ = exec.Wait(context.Background(), started.PID)
		close(done)
	}()

	if err := exec.Cancel(context.Background(), started.PGID, 200*time.Millisecond); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("process was not reaped after Cancel")
	}

	if local.ProcessGroupAlive(started.PGID) {
		t.Error("expected the process group to be dead after Cancel")
	}
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
