package archtest

import "testing"

// forbiddenEdges is a data-driven table of "importer -> forbidden import"
// edges. It grows by one entry per later document that introduces a new
// boundary (e.g. "internal/workspace only reaches git through the
// executor"); seeded here with the one edge L00 can state truthfully.
var forbiddenEdges = []struct {
	name      string
	forbidden string
	exempt    string // the package allowed to import it, if any
}{
	{name: "nothing imports internal/api (it is a leaf)", forbidden: modulePath + "/internal/api", exempt: modulePath + "/internal/api"},
}

func TestArchitecture_dependencyDirection(t *testing.T) {
	t.Run("realTree", func(t *testing.T) {
		for _, pkg := range loadPkgs(t, nil, "./...") {
			for _, edge := range forbiddenEdges {
				if pkg.PkgPath == edge.exempt {
					continue
				}
				if hit := importsAny(pkg, edge.forbidden); len(hit) > 0 {
					t.Errorf("%s: %s imports %s", edge.name, pkg.PkgPath, edge.forbidden)
				}
			}
		}
	})

	t.Run("fixtureIsCaught", func(t *testing.T) {
		found := false
		for _, pkg := range loadPkgs(t, []string{"violation"}, "./internal/archtest/fixtures/dependencydirection") {
			if hit := importsAny(pkg, modulePath+"/internal/api"); len(hit) > 0 {
				found = true
			}
		}
		if !found {
			t.Fatal("expected the dependencydirection fixture to be flagged, but the checker passed it")
		}
	})
}
