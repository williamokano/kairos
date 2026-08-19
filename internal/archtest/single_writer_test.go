package archtest

import (
	"go/ast"
	"testing"

	"golang.org/x/tools/go/packages"
)

// appendIfTouchesWriterDB reports whether pkg defines a method named
// AppendIf whose body references an identifier named writerDB — i.e.
// whether the public write entrypoint bypasses the single-writer queue and
// touches the write *sql.DB directly instead of only ever sending to the
// request channel the writer goroutine owns exclusively
// (06-durability.md: "All writes funnel through one goroutine").
func appendIfTouchesWriterDB(pkg *packages.Package) bool {
	found := false
	for _, file := range pkg.Syntax {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "AppendIf" || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && id.Name == "writerDB" {
					found = true
				}
				return true
			})
		}
	}
	return found
}

func TestArchitecture_singleWriter(t *testing.T) {
	t.Run("realTree", func(t *testing.T) {
		for _, pkg := range loadPkgs(t, nil, "./internal/eventstore/...") {
			if appendIfTouchesWriterDB(pkg) {
				t.Errorf("%s: AppendIf touches writerDB directly; it must only send to the writer goroutine's request channel", pkg.PkgPath)
			}
		}
	})

	t.Run("fixtureIsCaught", func(t *testing.T) {
		found := false
		for _, pkg := range loadPkgs(t, []string{"violation"}, "./internal/archtest/fixtures/singlewriter") {
			if appendIfTouchesWriterDB(pkg) {
				found = true
			}
		}
		if !found {
			t.Fatal("expected the singlewriter fixture to be flagged, but the checker passed it")
		}
	})
}
