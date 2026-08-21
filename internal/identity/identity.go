// Package identity is a deliberate, narrow divergence from
// 10-webui.md's stated "no multi-user/accounts" non-goal — the user
// explicitly asked, after this whole project was built without it, for
// output/sessions/executions to be attributed to whoever created them.
// This is attribution ONLY: there is no login, no password, no access
// control. Every user can still see and act on everything, exactly as
// before. See L25-projects-sessions.md's Documented decisions.
package identity

import (
	"net/http"
	"os/user"
	"strings"
)

// FromEnv resolves the CLI's identity: $KAIROS_USER if set, else the real
// OS username via os/user, else "" (never an error — attribution is a
// courtesy, not a requirement; an empty author is handled identically to
// every message recorded before this package existed).
func FromEnv(envUser string) string {
	if envUser != "" {
		return envUser
	}
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return ""
}

// HeaderName is the web UI's identity header — set client-side from a
// display name the browser remembers in localStorage (internal/web's
// app.js), never a cookie/session token: this is a courtesy label, not
// an authentication credential, so it is sent and trusted exactly like
// any other client-supplied form field.
const HeaderName = "X-Kairos-User"

// FromRequest reads the web UI's identity header, trimmed. Empty if
// absent — callers treat that identically to FromEnv's own "" case.
func FromRequest(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get(HeaderName))
}
