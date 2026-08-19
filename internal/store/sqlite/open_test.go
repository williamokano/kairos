package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/williamokano/kairos/internal/store/sqlite"
)

func TestOpen_writerModeIsSingleConnection(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "kairos.db"), sqlite.ModeWriter)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("MaxOpenConnections = %d, want 1", got)
	}
}

func TestOpen_readerModeAllowsMultipleConnections(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "kairos.db"), sqlite.ModeReader)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	if got := db.Stats().MaxOpenConnections; got != 8 {
		t.Errorf("MaxOpenConnections = %d, want 8", got)
	}
}

func TestOpen_pragmasAreEffective(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "kairos.db"), sqlite.ModeWriter)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	var journalMode string
	if err := db.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("querying journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want wal", journalMode)
	}

	var foreignKeys int
	if err := db.QueryRowContext(context.Background(), "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("querying foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Errorf("foreign_keys = %d, want 1", foreignKeys)
	}
}
