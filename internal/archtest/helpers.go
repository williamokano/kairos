// Package archtest holds Kairos's architecture tests — the checks that hold
// package boundaries the design depends on, mechanically rather than by
// convention (AGENTS.md §9). Each real check pairs a "the real tree is
// clean" assertion with a "the checker catches its own violation fixture"
// assertion, because an architecture test nobody has seen fail is a test
// nobody knows works.
package archtest

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

const modulePath = "github.com/williamokano/kairos"

// repoRoot returns the module root, resolved from this file's own location
// so tests work regardless of the caller's working directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolving repo root: runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// loadPkgs loads Go packages matching patterns, optionally built with extra
// tags (used to pull in `violation` fixtures that are excluded from normal
// builds).
func loadPkgs(t *testing.T, tags []string, patterns ...string) []*packages.Package {
	t.Helper()
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedImports |
			packages.NeedDeps | packages.NeedSyntax | packages.NeedTypes |
			packages.NeedTypesInfo,
		Dir: repoRoot(t),
	}
	if len(tags) > 0 {
		cfg.BuildFlags = []string{"-tags=" + strings.Join(tags, ",")}
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		t.Fatalf("loading packages %v: %v", patterns, err)
	}
	if packages.PrintErrors(pkgs) > 0 {
		t.Fatalf("package load errors in %v", patterns)
	}
	if len(pkgs) == 0 {
		t.Fatalf("no packages matched %v", patterns)
	}
	return pkgs
}

// importsAny returns the subset of forbidden import paths that pkg directly imports.
func importsAny(pkg *packages.Package, forbidden ...string) []string {
	var hit []string
	for _, f := range forbidden {
		if _, ok := pkg.Imports[f]; ok {
			hit = append(hit, f)
		}
	}
	return hit
}

// importsWithPrefix returns the direct imports of pkg that start with prefix.
func importsWithPrefix(pkg *packages.Package, prefix string) []string {
	var hit []string
	for imp := range pkg.Imports {
		if strings.HasPrefix(imp, prefix) {
			hit = append(hit, imp)
		}
	}
	return hit
}

// importsInternal returns the internal/ import paths (under this module)
// that pkg directly imports.
func importsInternal(pkg *packages.Package) []string {
	return importsWithPrefix(pkg, modulePath+"/internal/")
}

// callsFunc reports whether pkg's syntax contains a call resolving (via type
// info) to one of funcNames in package pkgPath, e.g. callsFunc(pkg, "os",
// "Exit") for os.Exit(...).
func callsFunc(pkg *packages.Package, pkgPath string, funcNames ...string) bool {
	want := make(map[string]bool, len(funcNames))
	for _, n := range funcNames {
		want[n] = true
	}
	found := false
	for _, file := range pkg.Syntax {
		ast.Inspect(file, func(n ast.Node) bool {
			if found {
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			pn, ok := pkg.TypesInfo.Uses[ident].(*types.PkgName)
			if !ok {
				return true
			}
			if pn.Imported().Path() == pkgPath && want[sel.Sel.Name] {
				found = true
			}
			return true
		})
	}
	return found
}
