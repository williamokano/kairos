//go:build violation

// Package nosqloutsidestore is a deliberate architecture-test violation
// fixture: it imports database/sql outside internal/store/sqlite, which
// TestArchitecture_noSQLOutsideStore must catch.
package nosqloutsidestore

import "database/sql"

var _ = sql.ErrNoRows
