package archtest

import (
	"net/http"
	"testing"

	"github.com/williamokano/kairos/internal/api"
)

// TestArchitecture_agentSocketRouteSubset (named since AGENTS.md §9,
// stubbed since L04, real as of L20): api.NewAgentMux's route table must
// be a strict subset of api.NewMux's — every route named in
// api.AgentSocketForbiddenPatterns must 404 on the agent socket while
// resolving normally on the admin socket, proving the narrower table is
// real rather than accidentally identical. See agentsocket.go's doc
// comment for why the agent socket's honest current content is a single
// liveness check.
func TestArchitecture_agentSocketRouteSubset(t *testing.T) {
	agentMux := api.NewAgentMux(api.Deps{})
	adminMux := api.NewMux(api.Deps{})

	for _, forbidden := range api.AgentSocketForbiddenPatterns {
		t.Run(forbidden.Method+" "+forbidden.Path, func(t *testing.T) {
			agentReq, err := http.NewRequest(forbidden.Method, forbidden.Path, nil)
			if err != nil {
				t.Fatalf("building request: %v", err)
			}
			if _, pattern := agentMux.Handler(agentReq); pattern != "" {
				t.Errorf("agent socket resolves %s %s (pattern %q) — must be absent, not merely unused", forbidden.Method, forbidden.Path, pattern)
			}

			adminReq, err := http.NewRequest(forbidden.Method, forbidden.Path, nil)
			if err != nil {
				t.Fatalf("building request: %v", err)
			}
			if _, pattern := adminMux.Handler(adminReq); pattern == "" {
				t.Errorf("admin socket does not resolve %s %s — the forbidden-list fixture itself is stale", forbidden.Method, forbidden.Path)
			}
		})
	}

	// The one route the agent socket DOES carry must still resolve.
	statusReq, _ := http.NewRequest(http.MethodGet, "/status", nil)
	if _, pattern := agentMux.Handler(statusReq); pattern == "" {
		t.Error("agent socket does not resolve GET /status — its one intended route is missing")
	}
}
