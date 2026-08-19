package archtest

import "testing"

// execForbiddenImports is the set only internal/executor/local may import
// (L4′: execution has exactly one chokepoint). golang.org/x/sys is not yet a
// dependency (added when the local executor lands in L06), so it is not
// checked here.
var execForbiddenImports = []string{"os/exec", "syscall"}

const executorLocalPkg = modulePath + "/internal/executor/local"

func TestArchitecture_noExecOutsideExecutor(t *testing.T) {
	t.Run("realTree", func(t *testing.T) {
		for _, pkg := range loadPkgs(t, nil, "./...") {
			if pkg.PkgPath == executorLocalPkg {
				continue
			}
			if hit := importsAny(pkg, execForbiddenImports...); len(hit) > 0 {
				t.Errorf("%s imports forbidden packages: %v (only %s may)", pkg.PkgPath, hit, executorLocalPkg)
			}
		}
	})

	t.Run("fixtureIsCaught", func(t *testing.T) {
		found := false
		for _, pkg := range loadPkgs(t, []string{"violation"}, "./internal/archtest/fixtures/noexecoutsideexecutor") {
			if hit := importsAny(pkg, execForbiddenImports...); len(hit) > 0 {
				found = true
			}
		}
		if !found {
			t.Fatal("expected the noexecoutsideexecutor fixture to be flagged, but the checker passed it")
		}
	})
}
