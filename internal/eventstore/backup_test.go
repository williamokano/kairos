package eventstore_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/events"
	"github.com/williamokano/kairos/internal/eventstore"
)

// TestStore_backupProducesARestorableQueryableCopy proves Backup is the
// real thing, not "the file exists": open the backup as an independent
// store and confirm Verify passes and the run history is intact.
func TestStore_backupProducesARestorableQueryableCopy(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	meta := eventstore.AppendMeta{Actor: "test", CorrelationID: "c1", OccurredAt: time.Unix(0, 0)}

	if _, err := st.AppendIf(ctx, "run_1", 0, []domain.Event{
		domain.TriggerReceived{RunID: "run_1", TriggerRef: "test", DefinitionRef: "def@1", CorrelationID: "run_1"},
	}, meta); err != nil {
		t.Fatalf("AppendIf: %v", err)
	}

	dir := t.TempDir()
	backupPath := filepath.Join(dir, "backup.db")
	if err := st.Backup(ctx, backupPath); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("backup file missing: %v", err)
	}

	registry, err := events.Builtin()
	if err != nil {
		t.Fatalf("Builtin: %v", err)
	}
	restored, err := eventstore.Open(ctx, eventstore.Config{
		Path:     backupPath,
		Registry: registry,
		Projections: []eventstore.Projection{
			eventstore.RunStateProjection{},
			eventstore.RunIndexProjection{},
		},
	})
	if err != nil {
		t.Fatalf("opening backup as a store: %v", err)
	}
	defer func() { _ = restored.Close() }()

	report, err := restored.Verify(ctx)
	if err != nil {
		t.Fatalf("Verify on restored copy: %v", err)
	}
	if len(report.MismatchedRunIDs) != 0 {
		t.Fatalf("restored copy failed verify: %v", report.MismatchedRunIDs)
	}

	envs, err := restored.Read(ctx, "run_1")
	if err != nil {
		t.Fatalf("Read on restored copy: %v", err)
	}
	if len(envs) != 1 {
		t.Fatalf("restored copy has %d events for run_1, want 1", len(envs))
	}
}

// TestStore_backupRefusesToOverwriteAnExistingFile confirms VACUUM INTO's
// own refuse-to-overwrite behaviour surfaces as a real error, not a
// silent partial write.
func TestStore_backupRefusesToOverwriteAnExistingFile(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	dir := t.TempDir()
	backupPath := filepath.Join(dir, "backup.db")
	if err := os.WriteFile(backupPath, []byte("not a database"), 0o600); err != nil {
		t.Fatalf("seeding existing file: %v", err)
	}

	if err := st.Backup(ctx, backupPath); err == nil {
		t.Fatal("expected Backup to refuse overwriting an existing file, got nil error")
	}
}
