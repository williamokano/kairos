package archtest

import "testing"

const mainPkg = modulePath + "/cmd/kairos"

func TestArchitecture_noOsExitOutsideMain(t *testing.T) {
	t.Run("realTree", func(t *testing.T) {
		for _, pkg := range loadPkgs(t, nil, "./...") {
			if pkg.PkgPath == mainPkg {
				continue
			}
			if callsFunc(pkg, "os", "Exit") || callsFunc(pkg, "log", "Fatal", "Fatalf", "Fatalln") {
				t.Errorf("%s calls os.Exit or log.Fatal*; only %s may", pkg.PkgPath, mainPkg)
			}
		}
	})

	t.Run("fixtureIsCaught", func(t *testing.T) {
		found := false
		for _, pkg := range loadPkgs(t, []string{"violation"}, "./internal/archtest/fixtures/noosexitoutsidemain") {
			if callsFunc(pkg, "os", "Exit") {
				found = true
			}
		}
		if !found {
			t.Fatal("expected the noosexitoutsidemain fixture to be flagged, but the checker passed it")
		}
	})
}
