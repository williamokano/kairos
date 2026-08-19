package archtest

import "testing"

// database/sql is stdlib type vocabulary, not the driver: internal/eventstore
// needs *sql.Tx/*sql.DB in the Projection interface (12-build-plan.md's
// "day one of L02" note explicitly sanctions this), so it is exempt from
// this half of the check alongside internal/store/sqlite itself.
var sqlForbiddenImports = []string{"database/sql"}

// modernc.org/sqlite is the actual driver: it stays importable only by
// internal/store/sqlite, with NO exemption for internal/eventstore.
var sqliteDriverForbiddenImports = []string{"modernc.org/sqlite"}

const sqliteStorePkg = modulePath + "/internal/store/sqlite"
const eventstorePkg = modulePath + "/internal/eventstore"

func TestArchitecture_noSQLOutsideStore(t *testing.T) {
	t.Run("realTree", func(t *testing.T) {
		for _, pkg := range loadPkgs(t, nil, "./...") {
			if hit := importsAny(pkg, sqliteDriverForbiddenImports...); len(hit) > 0 && pkg.PkgPath != sqliteStorePkg {
				t.Errorf("%s imports forbidden packages: %v (only %s may)", pkg.PkgPath, hit, sqliteStorePkg)
			}
			if pkg.PkgPath == sqliteStorePkg || pkg.PkgPath == eventstorePkg {
				continue
			}
			if hit := importsAny(pkg, sqlForbiddenImports...); len(hit) > 0 {
				t.Errorf("%s imports forbidden packages: %v (only %s and %s may)", pkg.PkgPath, hit, sqliteStorePkg, eventstorePkg)
			}
		}
	})

	t.Run("fixtureIsCaught", func(t *testing.T) {
		found := false
		for _, pkg := range loadPkgs(t, []string{"violation"}, "./internal/archtest/fixtures/nosqloutsidestore") {
			if hit := importsAny(pkg, sqlForbiddenImports...); len(hit) > 0 {
				found = true
			}
		}
		if !found {
			t.Fatal("expected the nosqloutsidestore fixture to be flagged, but the checker passed it")
		}
	})

	t.Run("driverFixtureIsCaught", func(t *testing.T) {
		found := false
		for _, pkg := range loadPkgs(t, []string{"violation"}, "./internal/archtest/fixtures/sqlitedriveroutsidestore") {
			if hit := importsAny(pkg, sqliteDriverForbiddenImports...); len(hit) > 0 {
				found = true
			}
		}
		if !found {
			t.Fatal("expected the sqlitedriveroutsidestore fixture to be flagged, but the checker passed it")
		}
	})
}
