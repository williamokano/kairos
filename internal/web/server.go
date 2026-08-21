// Package web is the daemon's third client surface (10-webui.md), a
// browser-facing peer of internal/tui and internal/cli — it talks to the
// daemon ONLY through internal/cli.Client's existing HTTP-over-unix-socket
// API, exactly as the TUI does (ADR 0008: "terminal is a client"; this
// package is "the browser is a client" for the identical reason). It never
// imports internal/api or internal/engine, so nothing here can grow a
// private code path the CLI/TUI don't also have — see
// TestUI_everyCallHasCLICounterpart's now-three-way parity discipline.
//
// Served from the daemon process itself, on a SEPARATE listener from the
// unix admin socket: a loopback TCP port (10-webui.md: "http://127.0.0.1:7717"),
// because a browser cannot dial a unix socket. Binding non-loopback refuses
// by default (see Listen's doc comment) — this is the "one CSRF away from
// arbitrary code execution" surface the doc's Auth section names, so every
// control in that table is implemented here for real, not as a TODO.
package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/williamokano/kairos/internal/cli"
)

// Deps is what every web handler needs.
type Deps struct {
	Client *cli.Client
	// SockPath is the daemon's admin unix socket — used ONLY by the SSE
	// passthrough (frag.go's handleRunEvents), which must forward the
	// daemon's raw chunked SSE response (headers, flush timing, and
	// Last-Event-ID semantics all intact) rather than going through
	// cli.Client's bounded, buffering Events() call. Every other handler
	// in this package uses Client exclusively.
	SockPath string
	// Token is the per-`kairos serve` random bearer token (32 bytes, hex
	// encoded) — see GenerateToken. Accepted as `Authorization: Bearer` or
	// exchanged once via `?t=` for a session cookie (10-webui.md's auth
	// table, row 2).
	Token string
	// AllowedHosts is the Host-header allowlist (row 3) — normally
	// {"127.0.0.1:<port>", "localhost:<port>"}.
	AllowedHosts []string
	// NoAuth, when true, bypasses the token/cookie/bearer check entirely
	// — set ONLY when the caller supplied the exact RequiredNoAuthAck
	// string (checked once at boot, in cmd/kairos/serve.go, matching
	// Listen's RequiredNonLoopbackAck pattern), for a user running behind
	// their own auth layer (e.g. Cloudflare Access) who wants Kairos's
	// own token exchange gone. The Host-allowlist and Origin/
	// Sec-Fetch-Site checks are NOT about identity — they stay on
	// unconditionally regardless of this flag (see withSecurity).
	NoAuth bool
}

// RequiredNoAuthAck is the exact acknowledgement string that must be
// supplied (via KAIROS_WEB_NO_AUTH_ACK, checked in cmd/kairos/serve.go)
// to run with Deps.NoAuth true — auth stays ON by default; this is a
// narrow, explicit, documented escape hatch, not a new default, matching
// RequiredNonLoopbackAck's exact pattern one function up.
const RequiredNoAuthAck = "yes-i-have-my-own-auth-in-front"

// GenerateToken returns a fresh 32-byte, hex-encoded token — regenerated
// every `kairos serve`, per 10-webui.md's auth row 2. Callers persist it to
// $KAIROS_HOME/web-token at mode 0600; this package never touches the
// filesystem itself (cmd/kairos, the composition root, owns that, matching
// every other document's file-ownership convention).
func GenerateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating web token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

const sessionCookieName = "kairos_web_session"

// NewMux builds the web UI's http.Handler: routing plus every auth control
// 10-webui.md's table names, wrapping every route.
func NewMux(deps Deps) http.Handler {
	return withSecurity(deps, NewRawMux(deps))
}

// NewRawMux builds the route table alone, with no auth wrapping —
// exported for internal/archtest's route-parity introspection
// (apispec.Op.WebPath resolves against this mux's *http.ServeMux.Handler,
// which Go 1.22+'s pattern-based routing exposes), and for tests that
// want to drive handlers directly without minting tokens/cookies.
// Production code should call NewMux, never this.
func NewRawMux(deps Deps) *http.ServeMux {
	mux := http.NewServeMux()
	registerPages(mux, deps)
	registerFragments(mux, deps)
	registerMutations(mux, deps)
	registerChatRoutes(mux, deps)
	registerSessionChatRoutes(mux, deps)
	registerFSBrowseFragment(mux, deps)
	registerStatic(mux)
	return mux
}

// withSecurity wraps every request with, in order: the CSP/security
// headers (row 5, applied unconditionally including to error responses),
// the Host allowlist (row 3, blocks DNS rebinding), authentication (row 2),
// and — for mutating methods only — the Origin/Sec-Fetch-Site check (row
// 4). Each is a real, tested rejection, not documentation.
func withSecurity(deps Deps, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w)

		if !hostAllowed(r.Host, deps.AllowedHosts) {
			http.Error(w, "host not allowed", http.StatusForbidden)
			return
		}

		if deps.NoAuth {
			// Identity (token/cookie/bearer) is skipped entirely — the
			// operator declared their own auth layer sits in front. The
			// Host allowlist above and the Origin/Sec-Fetch-Site check
			// below are NOT about identity (they block DNS rebinding and
			// cross-site mutation respectively) and still apply
			// unconditionally.
			if isMutating(r.Method) && !originAllowed(r, deps.AllowedHosts) {
				http.Error(w, "origin not allowed", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		// The one-time `?t=` exchange: valid token in the query string sets
		// the session cookie and 302s to the same path with the query
		// stripped, so the token never lands in browser history or a
		// pasted URL (10-webui.md's auth row 2, explicit requirement).
		if t := r.URL.Query().Get("t"); t != "" {
			if !validToken(t, deps.Token) {
				http.Error(w, "invalid token", http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{
				Name: sessionCookieName, Value: deps.Token,
				HttpOnly: true, SameSite: http.SameSiteStrictMode, Path: "/",
			})
			u := *r.URL
			q := u.Query()
			q.Del("t")
			u.RawQuery = q.Encode()
			http.Redirect(w, r, u.RequestURI(), http.StatusFound)
			return
		}

		if !authenticated(r, deps.Token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		if isMutating(r.Method) && !originAllowed(r, deps.AllowedHosts) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func setSecurityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; img-src 'self' data:; frame-ancestors 'none'")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
}

func hostAllowed(host string, allowed []string) bool {
	for _, a := range allowed {
		if host == a {
			return true
		}
	}
	return false
}

func authenticated(r *http.Request, token string) bool {
	if token == "" {
		return false // an empty configured token never authenticates anything
	}
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		if validToken(strings.TrimPrefix(auth, "Bearer "), token) {
			return true
		}
	}
	if c, err := r.Cookie(sessionCookieName); err == nil {
		if validToken(c.Value, token) {
			return true
		}
	}
	return false
}

// validToken uses a constant-time comparison — a token-guessing timing
// side-channel is exactly the kind of thing this auth surface exists to
// close.
func validToken(got, want string) bool {
	return len(got) == len(want) && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func isMutating(method string) bool {
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete
}

// originAllowed enforces 10-webui.md's row 4: Origin and Sec-Fetch-Site are
// checked on every mutating request, never relying on SameSite alone. A
// request with neither header (e.g. a same-origin form post from an older
// browser) is allowed only if Origin is simply absent AND Sec-Fetch-Site is
// absent too — both being absent is consistent with a non-browser client
// (curl with the bearer token), which the Host-allowlist and bearer-token
// checks already gate; if either header IS present, it must say same-origin.
func originAllowed(r *http.Request, allowedHosts []string) bool {
	if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" && sfs != "same-origin" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return hostAllowed(u.Host, allowedHosts)
}

// Listen binds addr and returns a listener, refusing a non-loopback
// address unless ack is the exact acknowledgement string 10-webui.md's
// auth row 1 specifies — the same "type the specific string" pattern L13's
// --unattended and L06's isolation acknowledgement already established,
// deliberately reused rather than reinvented.
const RequiredNonLoopbackAck = "yes-lan-only-behind-a-firewall"

func Listen(addr, ack string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("parsing web listen address %q: %w", addr, err)
	}
	if !isLoopback(host) && ack != RequiredNonLoopbackAck {
		return nil, fmt.Errorf(
			"refusing to bind the web UI to non-loopback address %q without "+
				"iUnderstandThisExposesAgentControl: %q (10-webui.md's auth row 1)",
			addr, RequiredNonLoopbackAck)
	}
	return net.Listen("tcp", addr)
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// registerStatic serves the embedded (or, under -tags dev, live) static
// assets — htmx, its SSE extension, app.css, app.js — all vendored files,
// never a CDN reference, so the CSP's default-src 'self' holds.
func registerStatic(mux *http.ServeMux) {
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS()))))
	// chroma.css is generated from the pinned chroma style (diffrender.go)
	// rather than hand-vendored, so it can never name a class the running
	// binary's chroma version doesn't actually emit.
	mux.HandleFunc("GET /static/chroma.css", handleChromaCSS)
}
