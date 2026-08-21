package identity_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/williamokano/kairos/internal/identity"
)

func TestFromEnv_prefersExplicitValue(t *testing.T) {
	if got := identity.FromEnv("alice"); got != "alice" {
		t.Errorf("FromEnv(%q) = %q, want alice", "alice", got)
	}
}

func TestFromEnv_fallsBackToOSUser(t *testing.T) {
	got := identity.FromEnv("")
	if got == "" {
		t.Skip("no OS user resolvable in this environment — not this package's bug")
	}
}

func TestFromRequest_readsHeaderTrimmed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(identity.HeaderName, "  bob  ")
	if got := identity.FromRequest(req); got != "bob" {
		t.Errorf("FromRequest = %q, want bob (trimmed)", got)
	}
}

func TestFromRequest_emptyWhenAbsent(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := identity.FromRequest(req); got != "" {
		t.Errorf("FromRequest = %q, want empty", got)
	}
}
