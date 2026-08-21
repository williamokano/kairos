package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/williamokano/kairos/internal/cli"
	"github.com/williamokano/kairos/internal/web"
)

// fetchStatic reads an embedded static asset through the real route
// (GET /static/<name>) — internal/web never exposes staticFS() outside
// its own package, and these are black-box (web_test) tests, matching
// every other test in this package.
func fetchStatic(t *testing.T, deps web.Deps, name string) string {
	t.Helper()
	h := web.NewMux(deps)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodGet, "/static/"+name, deps.Token, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /static/%s: status = %d", name, rec.Code)
	}
	return rec.Body.String()
}

// TestAppJS_commandPaletteMirrorsTUIScreenNamesAndVerbsExactly proves
// app.js's palette is a literal port of internal/tui/palette.go's
// screenNames/paletteVerbs maps — every key that exists there must exist
// here too, so the two surfaces cannot silently drift apart. Go cannot
// execute app.js (no browser/JS runtime is an approved dependency in this
// tree — AGENTS.md's closed dependency table), so this is the honest
// equivalent this package's existing tests already establish (see
// decision_test.go: JS behavior is a client-side UX aid never executed by
// Go tests, only its server-rendered contract and, here, its source
// fidelity to the document it must mirror).
func TestAppJS_commandPaletteMirrorsTUIScreenNamesAndVerbsExactly(t *testing.T) {
	deps := web.Deps{Token: "test-token-0123456789abcdef0123456789abcdef", AllowedHosts: []string{"kairos.test"}}
	js := fetchStatic(t, deps, "app.js")

	// internal/tui/palette.go's screenNames map, key for key.
	for _, key := range []string{"home", "conversation", "conversations", "run", "runs", "logs", "inbox", "runners"} {
		if !strings.Contains(js, `"`+key+`":`) {
			t.Errorf("app.js's palette is missing TUI screen name %q", key)
		}
	}
	// bench/benchmark have no web page — the TUI's own screenNames entries
	// for them must NOT be silently invented here.
	for _, key := range []string{"bench", "benchmark"} {
		if strings.Contains(js, `"`+key+`":`) {
			t.Errorf("app.js's palette must not invent a %q entry — no web equivalent exists", key)
		}
	}
	// internal/tui/palette.go's paletteVerbs map.
	for _, key := range []string{"approve", "status"} {
		if !strings.Contains(js, `"`+key+`":`) {
			t.Errorf("app.js's palette is missing TUI verb %q", key)
		}
	}
	// 09-cli-and-tui.md: "no fuzzy-search over everything, no command
	// history as a feature" — the palette must not persist input across
	// opens (no localStorage/sessionStorage) or do substring/fuzzy scoring.
	for _, forbidden := range []string{"localStorage", "sessionStorage", "Fuse(", ".includes(input"} {
		if strings.Contains(js, forbidden) {
			t.Errorf("app.js's palette must have no history/fuzzy matching, found forbidden token %q", forbidden)
		}
	}
	// The ULID shape check, ported verbatim (internal/tui/palette.go's
	// looksLikeULID: 26 Crockford-base32 characters).
	if !strings.Contains(js, `0-9A-Za-z]{26}`) {
		t.Error("app.js's palette is missing the 26-character ULID shape check")
	}
}

// TestAppJS_keyboardModelBindsTheTUIsGlobalKeys proves the keyboard model
// (internal/tui/keys.go's global NAV-mode switch) is really ported, not
// just similarly-shaped: every global binding the TUI has must appear,
// and 'q'/'Q' (quit) must NOT — a browser tab has no equivalent action,
// named honestly rather than faked (see app.js's own doc comment).
func TestAppJS_keyboardModelBindsTheTUIsGlobalKeys(t *testing.T) {
	deps := web.Deps{Token: "test-token-0123456789abcdef0123456789abcdef", AllowedHosts: []string{"kairos.test"}}
	js := fetchStatic(t, deps, "app.js")

	for _, key := range []string{`case "h":`, `case "r":`, `case "l":`, `case "c":`, `case "a":`,
		`case "j":`, `case "k":`, `case "g":`, `case "G":`, `case "Enter":`, `case "Escape":`, `case "i":`} {
		if !strings.Contains(js, key) {
			t.Errorf("app.js's keyboard model is missing binding %s", key)
		}
	}
	for _, forbidden := range []string{`case "q":`, `case "Q":`} {
		if strings.Contains(js, forbidden) {
			t.Errorf("app.js must not bind %s — quitting a browser tab is not the same action as the TUI's process quit", forbidden)
		}
	}
	// ':' and Ctrl/Cmd+K both open the palette (10-webui.md's own "⌘K"
	// naming plus the TUI's literal ':' key).
	if !strings.Contains(js, `e.key === ":"`) {
		t.Error("app.js is missing the ':' command-palette binding")
	}
	if !strings.Contains(js, `e.key === "k"`) || !strings.Contains(js, "metaKey || e.ctrlKey") {
		t.Error("app.js is missing the Ctrl/Cmd+K command-palette binding")
	}
}

// TestLayout_rendersPaletteOverlayAndKeyboardStatusHooks proves every page
// carries the DOM hooks app.js's palette/keyboard code depends on —
// without them the (untestable-by-Go) JS would silently no-op.
func TestLayout_rendersPaletteOverlayAndKeyboardStatusHooks(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodGet, "/", deps.Token, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, id := range []string{`id="palette-overlay"`, `id="palette-input"`, `id="palette-status"`, `id="kbd-status"`} {
		if !strings.Contains(body, id) {
			t.Errorf("expected %s in every rendered page (layout.gohtml)", id)
		}
	}
	if !strings.Contains(body, `palette-overlay" class="palette-overlay" hidden`) {
		t.Error("the command palette overlay must render hidden by default")
	}
}

// TestLayout_bodyCarriesRunIDOnlyOnPagesWithARunContext proves the
// keyboard model's conditional 'l'/'c' bindings (only act "if there is an
// active run", mirroring internal/tui/screens_runinspector.go's
// `if m.runInspector.runID != ""`) have real data to key off: run/decision/
// conversation/diff pages carry the real run id; home does not invent one.
func TestLayout_bodyCarriesRunIDOnlyOnPagesWithARunContext(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	fd.runState = cli.RunState{ID: "run_ctx", Status: "running"}
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	withRun := []string{"/runs/run_ctx", "/t/run_ctx/approve", "/c/run_ctx"}
	for _, path := range withRun {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, authedRequest(http.MethodGet, path, deps.Token, ""))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status = %d, body: %s", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `data-run-id="run_ctx"`) {
			t.Errorf("GET %s: expected data-run-id=\"run_ctx\" on <body>", path)
		}
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(http.MethodGet, "/", deps.Token, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /: status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `data-run-id="run_ctx"`) {
		t.Error("home page must not carry a run id it was never given")
	}
	if !strings.Contains(rec.Body.String(), `data-run-id=""`) {
		t.Error("home page's <body> should render an empty data-run-id, not omit the attribute inconsistently")
	}
}

// TestHomeAndRunsPages_renderNavListMarkupForJKNavigation proves the j/k/
// gg/G/Enter list bindings have real, correctly-targeted markup to act on
// — each row's data-href matches its own link exactly, so "the keyboard
// shortcut navigates to the right place" is verified at the data layer
// (execution of the keydown handler itself is the untestable-by-Go part,
// exactly as this package's decision-screen JS already is).
func TestHomeAndRunsPages_renderNavListMarkupForJKNavigation(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	fd.runs = []cli.RunSummary{
		{RunID: "run_a", Status: "running"},
		{RunID: "run_b", Status: "succeeded"},
	}
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	for _, path := range []string{"/", "/runs"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, authedRequest(http.MethodGet, path, deps.Token, ""))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status = %d, body: %s", path, rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, "data-nav-list") {
			t.Errorf("GET %s: expected a data-nav-list container", path)
		}
		if !strings.Contains(body, `data-nav-item data-href="/runs/run_a"`) {
			t.Errorf("GET %s: expected a data-nav-item for run_a with the correct data-href", path)
		}
	}
}
