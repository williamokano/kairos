package archtest

import (
	"net/http"
	"strings"
	"testing"

	"github.com/williamokano/kairos/internal/apispec"
	"github.com/williamokano/kairos/internal/web"
)

// TestUI_webRoutesResolve extends TestUI_everyCallHasCLICounterpart's
// discipline to the third surface (L20): every apispec.Op that declares a
// WebPath must resolve to a real, registered handler in
// internal/web.NewRawMux — not merely a string that looks right in
// apispec/ops.go. Ops with an empty WebPath are skipped (10-webui.md
// names several routes this pass does not build — see
// L20-webui.md's Future work; an empty WebPath documents that honestly
// rather than faking an entry).
func TestUI_webRoutesResolve(t *testing.T) {
	mux := web.NewRawMux(web.Deps{})

	checked := 0
	for _, op := range apispec.Ops {
		if op.WebPath == "" {
			continue
		}
		checked++
		method, pattern, ok := strings.Cut(op.WebPath, " ")
		if !ok {
			t.Fatalf("malformed WebPath %q on Op %s %s", op.WebPath, op.Method, op.Path)
		}
		req, err := http.NewRequest(method, examplePath(pattern), nil)
		if err != nil {
			t.Fatalf("building request for %q: %v", op.WebPath, err)
		}
		if _, resolved := mux.Handler(req); resolved == "" || resolved == "404" {
			t.Errorf("apispec.Op %s %s declares WebPath %q, but no handler resolves it in internal/web", op.Method, op.Path, op.WebPath)
		}
	}
	if checked == 0 {
		t.Fatal("no apispec.Op declares a WebPath — this check would be vacuous")
	}
}

// examplePath substitutes every {param} segment in a route pattern with a
// concrete placeholder, so the resulting string is a valid request path a
// real ServeMux can match against.
func examplePath(pattern string) string {
	var b strings.Builder
	inParam := false
	for _, r := range pattern {
		switch {
		case r == '{':
			inParam = true
			b.WriteString("x")
		case r == '}':
			inParam = false
		case inParam:
			// skip param name characters
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
