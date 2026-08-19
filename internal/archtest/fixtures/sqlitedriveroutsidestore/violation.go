//go:build violation

// Package sqlitedriveroutsidestore is a deliberate architecture-test
// violation fixture: it imports modernc.org/sqlite outside
// internal/store/sqlite, which TestArchitecture_noSQLOutsideStore must
// catch.
package sqlitedriveroutsidestore

import _ "modernc.org/sqlite"
