package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/williamokano/kairos/internal/identity"
)

// TestClient_sendsUserIdentityHeader proves Client.User (set from
// identity.FromEnv(cfg.KairosUser) in ensureClient) is genuinely sent on
// the wire as identity.HeaderName — the whole mechanism `kairos
// do`/project/session attribution depends on — not just stored and never
// used.
func TestClient_sendsUserIdentityHeader(t *testing.T) {
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get(identity.HeaderName)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	client := &Client{http: srv.Client(), base: srv.URL, User: "alice"}
	if err := client.do(context.Background(), http.MethodGet, "/anything", nil, nil); err != nil {
		t.Fatalf("do: %v", err)
	}
	if gotHeader != "alice" {
		t.Errorf("received %s header = %q, want alice", identity.HeaderName, gotHeader)
	}
}

// TestClient_noIdentityHeaderWhenUserEmpty confirms an empty Client.User
// (every call before identity existed) sends no header at all, rather
// than an empty one — matching AppendMessageAs's own "empty behaves
// exactly like AppendMessage" contract on the receiving end.
func TestClient_noIdentityHeaderWhenUserEmpty(t *testing.T) {
	var sawHeader bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawHeader = r.Header[identity.HeaderName]
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	client := &Client{http: srv.Client(), base: srv.URL}
	if err := client.do(context.Background(), http.MethodGet, "/anything", nil, nil); err != nil {
		t.Fatalf("do: %v", err)
	}
	if sawHeader {
		t.Error("expected no identity header when Client.User is empty")
	}
}
