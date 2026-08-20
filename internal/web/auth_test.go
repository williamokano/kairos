package web_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/williamokano/kairos/internal/web"
)

func TestAuth_unauthenticatedRequestRejected(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "kairos.test"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestAuth_bearerTokenAccepted(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "kairos.test"
	req.Header.Set("Authorization", "Bearer "+deps.Token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
}

func TestAuth_wrongBearerTokenRejected(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "kairos.test"
	req.Header.Set("Authorization", "Bearer wrong-token-entirely-different-value")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestAuth_oneTimeTokenExchangeSetsSessionCookieAndStripsQuery(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	req := httptest.NewRequest(http.MethodGet, "/?t="+deps.Token, nil)
	req.Host = "kairos.test"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if loc != "/" {
		t.Errorf("redirect Location = %q, want the query stripped (\"/\")", loc)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Value != deps.Token {
		t.Errorf("expected exactly one session cookie carrying the token, got %+v", cookies)
	}
	if !cookies[0].HttpOnly {
		t.Error("session cookie is not HttpOnly")
	}
}

func TestAuth_invalidOneTimeTokenRejected(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	req := httptest.NewRequest(http.MethodGet, "/?t=not-the-real-token", nil)
	req.Host = "kairos.test"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestAuth_hostHeaderNotAllowlistedRejected(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "evil.example.com"
	req.Header.Set("Authorization", "Bearer "+deps.Token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestAuth_crossOriginMutationRejected(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	req := httptest.NewRequest(http.MethodPost, "/runs", nil)
	req.Host = "kairos.test"
	req.Header.Set("Authorization", "Bearer "+deps.Token)
	req.Header.Set("Origin", "http://evil.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestAuth_crossSiteSecFetchSiteRejected(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	req := httptest.NewRequest(http.MethodPost, "/runs", nil)
	req.Host = "kairos.test"
	req.Header.Set("Authorization", "Bearer "+deps.Token)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestAuth_cspHeaderPresent(t *testing.T) {
	fd, sockPath := newFakeDaemon(t)
	deps := testDeps(t, fd, sockPath)
	h := web.NewMux(deps)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "kairos.test"
	req.Header.Set("Authorization", "Bearer "+deps.Token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	csp := rec.Header().Get("Content-Security-Policy")
	if csp == "" || rec.Header().Get("Referrer-Policy") == "" {
		t.Errorf("missing security headers: CSP=%q Referrer-Policy=%q", csp, rec.Header().Get("Referrer-Policy"))
	}
}

func TestListen_refusesNonLoopbackWithoutAcknowledgement(t *testing.T) {
	if _, err := web.Listen("0.0.0.0:0", ""); err == nil {
		t.Error("expected an error binding a non-loopback address with no acknowledgement")
	}
}

func TestListen_acceptsNonLoopbackWithCorrectAcknowledgement(t *testing.T) {
	ln, err := web.Listen("0.0.0.0:0", web.RequiredNonLoopbackAck)
	if err != nil {
		t.Fatalf("expected the correct acknowledgement to be accepted: %v", err)
	}
	_ = ln.Close()
}

func TestListen_acceptsLoopbackWithNoAcknowledgement(t *testing.T) {
	ln, err := web.Listen("127.0.0.1:0", "")
	if err != nil {
		t.Fatalf("loopback should never require an acknowledgement: %v", err)
	}
	_ = ln.Close()
}
