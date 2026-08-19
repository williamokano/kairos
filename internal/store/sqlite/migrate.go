package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migration is one forward-only step, applied at most once.
type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations reads migrations/*.sql, sorted by their numeric prefix
// (e.g. "0001_events.sql" -> version 1).
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("reading migrations dir: %w", err)
	}
	migrations := make([]migration, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(e.Name(), "_")
		if !ok {
			return nil, fmt.Errorf("migration file %q has no version prefix", e.Name())
		}
		version, err := strconv.Atoi(prefix)
		if err != nil {
			return nil, fmt.Errorf("migration file %q has a non-numeric version prefix: %w", e.Name(), err)
		}
		body, err := migrationFS.ReadFile(path.Join("migrations", e.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading migration %q: %w", e.Name(), err)
		}
		migrations = append(migrations, migration{version: version, name: e.Name(), sql: string(body)})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })
	return migrations, nil
}

// Migrate applies every migration whose version exceeds the highest already
// recorded in schema_migrations, forward-only, each in its own transaction.
// Before applying anything to a database that already has a prior schema, it
// takes a hot, consistent backup via VACUUM INTO — "copying a live .db and
// losing the -wal is the number-one way people lose SQLite data"
// (06-durability.md).
func Migrate(ctx context.Context, db *sql.DB, backupDir string) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	) STRICT`); err != nil {
		return fmt.Errorf("creating schema_migrations: %w", err)
	}

	current, err := currentVersion(ctx, db)
	if err != nil {
		return err
	}

	all, err := loadMigrations()
	if err != nil {
		return err
	}

	var pending []migration
	for _, m := range all {
		if m.version > current {
			pending = append(pending, m)
		}
	}
	if len(pending) == 0 {
		return nil
	}

	if current > 0 && backupDir != "" {
		if err := backup(ctx, db, backupDir); err != nil {
			return fmt.Errorf("backing up before migration: %w", err)
		}
	}

	for _, m := range pending {
		if err := applyMigration(ctx, db, m); err != nil {
			return fmt.Errorf("applying migration %s: %w", m.name, err)
		}
	}
	return nil
}

func currentVersion(ctx context.Context, db *sql.DB) (int, error) {
	var version sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, fmt.Errorf("reading current schema version: %w", err)
	}
	return int(version.Int64), nil
}

func applyMigration(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("executing: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
		m.version, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("recording version: %w", err)
	}
	return tx.Commit()
}

func backup(ctx context.Context, db *sql.DB, backupDir string) error {
	dest := path.Join(backupDir, fmt.Sprintf("kairos-%d.db", time.Now().UTC().UnixNano()))
	_, err := db.ExecContext(ctx, `VACUUM INTO ?`, dest)
	return err
}
