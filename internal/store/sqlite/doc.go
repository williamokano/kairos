// Package sqlite is the only package allowed to import database/sql or the
// modernc.org/sqlite driver. Migrations, Open, and Migrate land here in L02.
//
// Empty until L02 (event store). It exists now so
// TestArchitecture_noSQLOutsideStore checks a real import graph.
package sqlite
