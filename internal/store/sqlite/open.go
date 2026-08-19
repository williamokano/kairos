// Package sqlite is the only package allowed to import database/sql or the
// modernc.org/sqlite driver (AGENTS.md §2). It owns connection-opening and
// forward-only migrations; it knows nothing about domain.Event or the event
// store's shape — that is internal/eventstore's job.
package sqlite

import (
	"database/sql"
	"fmt"
	"net/url"

	_ "modernc.org/sqlite" // registers the "sqlite" driver; imported here only
)

// ConnMode selects the PRAGMAs a connection is opened with.
type ConnMode int

const (
	// ModeWriter is the single writer connection: MaxOpenConns(1),
	// _txlock=immediate (06-durability.md: "SQLite's default deferred
	// transaction takes a read lock first and upgrades on first write,
	// and an upgrade under contention returns SQLITE_BUSY immediately
	// regardless of busy_timeout").
	ModeWriter ConnMode = iota
	// ModeReader is a read-only pool connection: MaxOpenConns(8), never
	// blocked by the writer thanks to WAL.
	ModeReader
)

// dsn builds the connection string with the PRAGMAs 06-durability.md
// specifies verbatim: WAL, synchronous=FULL, fullfsync (Darwin: without
// this, "FULL" lies on Apple SSDs), foreign_keys=ON (per-connection, off by
// default), busy_timeout=5000, temp_store=MEMORY, cache_size=-65536 (64
// MiB). ModeWriter additionally sets _txlock=immediate.
func dsn(path string, mode ConnMode) string {
	q := url.Values{}
	q.Set("_journal_mode", "WAL")
	q.Set("_synchronous", "FULL")
	q.Set("_foreign_keys", "on")
	q.Set("_busy_timeout", "5000")
	q.Add("_pragma", "fullfsync=1")
	q.Add("_pragma", "temp_store=MEMORY")
	q.Add("_pragma", "cache_size=-65536")
	if mode == ModeWriter {
		q.Set("_txlock", "immediate")
	}
	return "file:" + path + "?" + q.Encode()
}

// Open opens a SQLite connection pool at path in the given ConnMode. The
// caller is responsible for calling Migrate before using a ModeWriter
// connection for anything but migrations.
func Open(path string, mode ConnMode) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn(path, mode))
	if err != nil {
		return nil, fmt.Errorf("opening sqlite at %s: %w", path, err)
	}
	switch mode {
	case ModeWriter:
		db.SetMaxOpenConns(1)
	case ModeReader:
		db.SetMaxOpenConns(8)
	}
	return db, nil
}
