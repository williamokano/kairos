package archtest

import "testing"

// forbiddenEdges is a data-driven table of "importer -> forbidden import"
// edges. It grows by one entry per later document that introduces a new
// boundary (e.g. "internal/workspace only reaches git through the
// executor"); seeded here with the one edge L00 can state truthfully.
var forbiddenEdges = []struct {
	name      string
	forbidden string
	exempt    []string // packages allowed to import it
}{
	// internal/api is a leaf: no OTHER internal package may import it, so
	// two internal packages can never grow a hidden coupling through it.
	// cmd/kairos is exempt as the binary's composition root — the same
	// posture it already holds for os.Exit/os/exec/syscall — because
	// wiring the daemon's HTTP handlers together has to happen somewhere,
	// and main.go/serve.go's whole job is being that somewhere
	// (L04-daemon-api-cli.md's decision #4's sibling reasoning).
	{name: "nothing but cmd/kairos imports internal/api (it is a leaf)", forbidden: modulePath + "/internal/api", exempt: []string{modulePath + "/internal/api", mainPkg}},
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func TestArchitecture_dependencyDirection(t *testing.T) {
	t.Run("realTree", func(t *testing.T) {
		for _, pkg := range loadPkgs(t, nil, "./...") {
			for _, edge := range forbiddenEdges {
				if contains(edge.exempt, pkg.PkgPath) {
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
