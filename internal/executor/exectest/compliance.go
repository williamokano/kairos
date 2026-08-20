// Package exectest holds the compliance suite every local.Executor
// implementation must pass, and an in-memory Fake for engine unit tests
// that don't need real subprocesses.
package exectest

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/executor/local"
)

// RunComplianceSuite exercises the invariants any local.Executor must
// hold: the child runs in its own process group (never the test's), and
// its output goes to a real file, not a pipe. newExecutor is called once
// per subtest so state doesn't leak between them.
func RunComplianceSuite(t *testing.T, newExecutor func() *local.Local) {
	t.Helper()

	t.Run("childIsInItsOwnProcessGroup", func(t *testing.T) {
		exec := newExecutor()
		dir := t.TempDir()
		started, err := exec.Start(context.Background(), local.ExecSpec{
			Dir:  dir,
			Argv: []string{"sh", "-c", "echo hi; sleep 5"},
		})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer func() { _ = exec.Signal(context.Background(), started.PGID, local.SignalKill) }()

		ownPgid, err := syscall.Getpgid(os.Getpid())
		if err != nil {
			t.Fatalf("Getpgid(self): %v", err)
		}
		if started.PGID == ownPgid {
			t.Errorf("child pgid %d equals the test process's own pgid %d", started.PGID, ownPgid)
		}
		if started.PGID != started.PID {
			t.Errorf("PGID = %d, PID = %d; Setpgid with no explicit target pgid should make them equal", started.PGID, started.PID)
		}
	})

	t.Run("stdoutGoesToARegularFile", func(t *testing.T) {
		exec := newExecutor()
		dir := t.TempDir()
		started, err := exec.Start(context.Background(), local.ExecSpec{
			Dir:  dir,
			Argv: []string{"sh", "-c", "echo hello-exectest"},
		})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		if _, err := exec.Wait(context.Background(), started.PID); err != nil {
			t.Fatalf("Wait: %v", err)
		}

		info, err := os.Stat(filepath.Join(dir, "stdout.log"))
		if err != nil {
			t.Fatalf("stat stdout.log: %v", err)
		}
		if !info.Mode().IsRegular() {
			t.Errorf("stdout.log is not a regular file: mode %v", info.Mode())
		}
		if info.Size() == 0 {
			t.Error("stdout.log is empty, want the child's output")
		}
	})

	t.Run("procJSONIsWrittenBeforeReturn", func(t *testing.T) {
		exec := newExecutor()
		dir := t.TempDir()
		started, err := exec.Start(context.Background(), local.ExecSpec{
			Dir:  dir,
			Argv: []string{"sh", "-c", "true"},
		})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		_, _ = exec.Wait(context.Background(), started.PID)

		rec, ok, err := local.ReadProcRecord(dir)
		if err != nil {
			t.Fatalf("ReadProcRecord: %v", err)
		}
		if !ok {
			t.Fatal("expected proc.json to exist")
		}
		if rec.PID != started.PID || rec.PGID != started.PGID {
			t.Errorf("proc.json = %+v, want PID=%d PGID=%d", rec, started.PID, started.PGID)
		}
		if time.Since(rec.StartedAt) > time.Minute {
			t.Errorf("StartedAt = %v, looks stale", rec.StartedAt)
		}
	})
}
