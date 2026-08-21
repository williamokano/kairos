package web_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/williamokano/kairos/internal/cli"
	"github.com/williamokano/kairos/internal/web"
)

var errNoSnapshot = errors.New("engine: diff: no workspace snapshot recorded for this node: node never-wrote, run run_1")

// realUnifiedDiffFixture is a genuine `git diff --unified=3` patch for one
// Go file (line1.go), captured against a real repo by hand — a diff
// viewer test that only ever sees a made-up patch string proves nothing
// about parsePatch's hunk-header/line-prefix handling; this is the exact
// shape `git diff` actually emits.
const realUnifiedDiffFixture = `diff --git a/list.go b/list.go
index e69de29..8f94ac2 100644
--- a/list.go
+++ b/list.go
@@ -1,3 +1,4 @@
 package orders

-func List() {}
+func List() int {
+	return 1
+}
`

func TestDiffPage_rendersRealPatchWithHighlightingAndScopeBanner(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	fd.diffResult = cli.DiffResult{
		RunID: "run_1", NodeID: "n2", FromRef: "aaa111", ToRef: "bbb222",
		Files:           []cli.DiffFile{{Path: "list.go", Added: 3, Removed: 1}},
		Patch:           realUnifiedDiffFixture,
		WorkspacePaths:  []string{"notes/**"},
		ScopeViolations: []string{"list.go"},
	}
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodGet, "/runs/run_1/diff?node=n2", deps.Token, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if !strings.Contains(body, "list.go") {
		t.Errorf("expected the changed file path in the page, got: %s", body)
	}
	if !strings.Contains(body, "chroma") {
		t.Errorf("expected chroma-highlighted spans in the rendered diff, got no \"chroma\" class anywhere: %s", body)
	}
	if !strings.Contains(body, "scope-banner") || !strings.Contains(body, "notes/**") {
		t.Errorf("expected the scope-violation banner naming the declared scope, got: %s", body)
	}
	// The default mode is side-by-side (10-webui.md's own mockup default).
	if !strings.Contains(body, "diff-split") {
		t.Errorf("expected the side-by-side table by default, got: %s", body)
	}
}

func TestDiffPage_unifiedModeToggle(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	fd.diffResult = cli.DiffResult{
		RunID: "run_1", FromRef: "aaa111", ToRef: "bbb222",
		Files: []cli.DiffFile{{Path: "list.go", Added: 3, Removed: 1}},
		Patch: realUnifiedDiffFixture,
	}
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodGet, "/runs/run_1/diff?mode=unified", deps.Token, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "diff-hunk") {
		t.Errorf("expected unified-mode markup, got: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "diff-split") {
		t.Error("unified mode rendered the side-by-side table too")
	}
}

// TestDiffPage_deepLinkRedirectsToLineAnchor proves ?file=&line= resolves
// to a real 302 carrying a URL fragment matching an id the page actually
// renders — not just that the query params are accepted.
func TestDiffPage_deepLinkRedirectsToLineAnchor(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	fd.diffResult = cli.DiffResult{
		RunID: "run_1", FromRef: "aaa111", ToRef: "bbb222",
		Files: []cli.DiffFile{{Path: "list.go", Added: 3, Removed: 1}},
		Patch: realUnifiedDiffFixture,
	}
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodGet, "/runs/run_1/diff?file=list.go&line=3", deps.Token, ""))
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302, body: %s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "#f0-p") {
		t.Fatalf("Location = %q, want a fragment anchoring the deep-linked line", loc)
	}

	// The anchor must actually exist on the rendered page.
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, authedRequest(http.MethodGet, "/runs/run_1/diff", deps.Token, ""))
	frag := strings.TrimPrefix(loc[strings.Index(loc, "#"):], "#")
	if !strings.Contains(rec2.Body.String(), `id="`+frag+`"`) {
		t.Errorf("rendered page has no element with id=%q, deep link would land nowhere", frag)
	}
}

func TestDiffPage_noWorkspaceSnapshotDegradesToError(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	fd.diffErr = errNoSnapshot
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodGet, "/runs/run_1/diff?node=never-wrote", deps.Token, ""))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (the daemon's own not_found propagated as a load failure)", rec.Code)
	}
}
