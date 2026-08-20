package api

import "net/http"

// NewAgentMux builds the narrow, agent-facing route table this package's
// doc comment has named since L04 — a different socket from the
// admin-facing one NewMux builds, with a strictly smaller route table.
// 10-webui.md: "Agents get kairos check-output, artifact stage, and
// ask-human; they do not get approve, answer, publish, admin, or
// anything that starts a run."
//
// As of this document (L20), check-output validates a local file with no
// daemon round trip at all (L08's actor_llm.go sets $KAIROS_OUTPUT/
// $KAIROS_SCHEMA; internal/cli's check-output verb reads them directly —
// see checkoutput.go's doc comment), and artifact-stage/ask-human have no
// HTTP handler anywhere in this codebase yet — no document has built an
// agent-initiated daemon callback path. So this mux's honest current
// content is a single, genuinely safe, read-only liveness check
// (GET /status) an actor process could poll without any capability to
// approve, answer, publish, admin, start a run, or touch the event log —
// and, just as importantly, the explicit ABSENCE of every one of those.
// TestArchitecture_agentSocketRouteSubset asserts the absence, not merely
// documents it. Growing this mux with real agent-callback endpoints is
// Future work for whichever later document actually needs one.
func NewAgentMux(deps Deps) *http.ServeMux {
	mux := http.NewServeMux()
	registerStatusRoute(mux, deps)
	return mux
}

// AgentSocketForbiddenPatterns names route patterns that must NEVER
// appear on the agent socket — the single most important safety detail
// in this surface, per 10-webui.md. TestArchitecture_agentSocketRouteSubset
// asserts every one of these 404s against NewAgentMux while resolving
// normally against NewMux (the admin socket), proving the subset is real
// rather than accidental.
var AgentSocketForbiddenPatterns = []struct{ Method, Path string }{
	{"POST", "/runs"},
	{"POST", "/runs/x/approve"},
	{"POST", "/runs/x/conversation/messages"},
	{"POST", "/runs/x/fork"},
	{"POST", "/db/verify"},
	{"POST", "/db/rebuild"},
	{"POST", "/db/backup"},
	{"POST", "/sources"},
	{"POST", "/pause"},
	{"POST", "/resume"},
	{"POST", "/selfcheck"},
}
