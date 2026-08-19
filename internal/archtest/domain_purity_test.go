package archtest

import (
	"testing"

	"golang.org/x/tools/go/packages"
)

// domainForbiddenImports mirrors 01-architecture.md's list for
// TestArchitecture_domainPurity, minus time (checked separately: only
// time.Now is forbidden, not the package).
var domainForbiddenImports = []string{
	"os", "os/exec", "net", "database/sql", "syscall", "math/rand", "path/filepath",
}

func domainPurityProblems(pkg *packages.Package) []string {
	var problems []string
	problems = append(problems, importsAny(pkg, domainForbiddenImports...)...)
	problems = append(problems, importsInternal(pkg)...)
	if callsFunc(pkg, "time", "Now") {
		problems = append(problems, "time.Now")
	}
	return problems
}

func TestArchitecture_domainPurity(t *testing.T) {
	t.Run("realTree", func(t *testing.T) {
		for _, pkg := range loadPkgs(t, nil, "./internal/domain/...") {
			if problems := domainPurityProblems(pkg); len(problems) > 0 {
				t.Errorf("%s violates domain purity: %v", pkg.PkgPath, problems)
			}
		}
	})

	t.Run("fixtureIsCaught", func(t *testing.T) {
		found := false
		for _, pkg := range loadPkgs(t, []string{"violation"}, "./internal/archtest/fixtures/domainpurity") {
			if problems := domainPurityProblems(pkg); len(problems) > 0 {
				found = true
			}
		}
		if !found {
			t.Fatal("expected the domainpurity fixture to be flagged, but the checker passed it")
		}
	})
}
