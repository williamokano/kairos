package local_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/williamokano/kairos/internal/executor/local"
)

func TestProbe_matchingBootIDAndAliveProcessIsAlive(t *testing.T) {
	exec := local.New(local.DefaultBootIDProvider())
	dir := t.TempDir()
	started, err := exec.Start(context.Background(), local.ExecSpec{Dir: dir, Argv: []string{"sh", "-c", "sleep 5"}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = exec.Signal(context.Background(), started.PGID, local.SignalKill) }()

	rec, ok, err := local.ReadProcRecord(dir)
	if err != nil || !ok {
		t.Fatalf("ReadProcRecord: ok=%v err=%v", ok, err)
	}
	if got := local.Probe(rec, rec.BootID); got != local.VerdictAlive {
		t.Errorf("Probe = %v, want VerdictAlive", got)
	}
}

func TestProbe_deadProcessIsDead(t *testing.T) {
	exec := local.New(local.DefaultBootIDProvider())
	dir := t.TempDir()
	started, err := exec.Start(context.Background(), local.ExecSpec{Dir: dir, Argv: []string{"sh", "-c", "true"}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := exec.Wait(context.Background(), started.PID); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	rec, _, err := local.ReadProcRecord(dir)
	if err != nil {
		t.Fatalf("ReadProcRecord: %v", err)
	}
	if got := local.Probe(rec, rec.BootID); got != local.VerdictDead {
		t.Errorf("Probe = %v, want VerdictDead", got)
	}
}

func TestProbe_mismatchedBootIDIsUnverifiable(t *testing.T) {
	exec := local.New(local.DefaultBootIDProvider())
	dir := t.TempDir()
	started, err := exec.Start(context.Background(), local.ExecSpec{Dir: dir, Argv: []string{"sh", "-c", "sleep 5"}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = exec.Signal(context.Background(), started.PGID, local.SignalKill) }()

	rec, _, err := local.ReadProcRecord(dir)
	if err != nil {
		t.Fatalf("ReadProcRecord: %v", err)
	}
	if got := local.Probe(rec, "a-different-boot-id"); got != local.VerdictUnverifiable {
		t.Errorf("Probe = %v, want VerdictUnverifiable", got)
	}
}

func TestReadProcRecord_missingFileReturnsFalseNotError(t *testing.T) {
	_, ok, err := local.ReadProcRecord(filepath.Join(t.TempDir(), "nonexistent"))
	if err != nil {
		t.Fatalf("ReadProcRecord: %v", err)
	}
	if ok {
		t.Error("expected ok=false for a directory with no proc.json")
	}
}
