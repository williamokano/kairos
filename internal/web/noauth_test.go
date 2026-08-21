package web_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/williamokano/kairos/internal/web"
)

// TestNoAuth_bypassesTokenCheck is a real test for the user's explicit
// request — running behind their own auth layer (e.g. Cloudflare
// Access) — proving Deps.NoAuth genuinely skips the token/cookie/bearer
// check: no Authorization header, no cookie, no ?t=, and the request
// still succeeds.
func TestNoAuth_bypassesTokenCheck(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	deps := testDeps(t, fd, sockPath)
	deps.NoAuth = true
	h := web.NewMux(deps)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "kairos.test"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (NoAuth should bypass identity entirely)", rec.Code)
	}
}

// TestNoAuth_hostAndOriginChecksStillApply proves the two checks that
// are NOT about identity (Host allowlist, Origin/Sec-Fetch-Site on
// mutations) stay on unconditionally even with NoAuth — per Deps.NoAuth's
// own doc comment, this is deliberate: those checks block DNS rebinding
// and cross-site mutation, neither of which "I have my own auth in
// front" addresses.
func TestNoAuth_hostAndOriginChecksStillApply(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	deps := testDeps(t, fd, sockPath)
	deps.NoAuth = true
	h := web.NewMux(deps)

	t.Run("bad host still rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = "evil.example.com"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rec.Code)
		}
	})

	t.Run("cross-origin mutation still rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/runs", nil)
		req.Host = "kairos.test"
		req.Header.Set("Origin", "http://evil.example.com")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rec.Code)
		}
	})
}

// TestNoAuth_offByDefault confirms auth stays ON unless Deps.NoAuth is
// explicitly true — the zero value of web.Deps (as every caller before
// this feature existed constructs it) must still require identity.
func TestNoAuth_offByDefault(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	deps := testDeps(t, fd, sockPath) // NoAuth left false
	h := web.NewMux(deps)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "kairos.test"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 — auth must stay on by default", rec.Code)
	}
}
