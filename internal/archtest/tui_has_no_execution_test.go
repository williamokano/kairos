package archtest

import "testing"

func TestArchitecture_tuiHasNoExecution(t *testing.T) {
	t.Run("realTree", func(t *testing.T) {
		for _, pkg := range loadPkgs(t, nil, "./internal/tui/...") {
			if hit := importsAny(pkg, "os/exec"); len(hit) > 0 {
				t.Errorf("%s imports os/exec", pkg.PkgPath)
			}
			if hit := importsWithPrefix(pkg, modulePath+"/internal/executor/"); len(hit) > 0 {
				t.Errorf("%s imports executor package(s): %v", pkg.PkgPath, hit)
			}
		}
	})

	t.Run("fixtureIsCaught", func(t *testing.T) {
		found := false
		for _, pkg := range loadPkgs(t, []string{"violation"}, "./internal/archtest/fixtures/tuihasnoexecution") {
			if hit := importsAny(pkg, "os/exec"); len(hit) > 0 {
				found = true
			}
		}
		if !found {
			t.Fatal("expected the tuihasnoexecution fixture to be flagged, but the checker passed it")
		}
	})
}
