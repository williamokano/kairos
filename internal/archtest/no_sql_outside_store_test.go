package archtest

import "testing"

// sqlForbiddenImports mirrors 01-architecture.md: only internal/store/sqlite
// may import database/sql or the driver. modernc.org/sqlite is not yet a
// dependency (added when L02 lands), so only database/sql is checked here.
var sqlForbiddenImports = []string{"database/sql"}

const sqliteStorePkg = modulePath + "/internal/store/sqlite"

func TestArchitecture_noSQLOutsideStore(t *testing.T) {
	t.Run("realTree", func(t *testing.T) {
		for _, pkg := range loadPkgs(t, nil, "./...") {
			if pkg.PkgPath == sqliteStorePkg {
				continue
			}
			if hit := importsAny(pkg, sqlForbiddenImports...); len(hit) > 0 {
				t.Errorf("%s imports forbidden packages: %v (only %s may)", pkg.PkgPath, hit, sqliteStorePkg)
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
}
