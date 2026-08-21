// Package apispec is the neutral, zero-dependency source of truth mapping
// daemon API operations to CLI verbs. internal/api and internal/cli each
// consult it independently — neither imports the other — which is what
// keeps "nothing imports internal/api" true (internal/api stays a leaf,
// per AGENTS.md §2) while giving TestUI_everyCallHasCLICounterpart
// (AGENTS.md §9) something concrete to walk, real from L04 rather than
// retrofitted once a web UI (L20) exists.
package apispec

// Op is one daemon API operation, the CLI verb that exercises it, and
// (L20) the web UI route pattern that exercises it, if a web page or
// mutation for it exists yet. WebPath is deliberately optional — most
// Ops predate the web UI and 10-webui.md's own ~50-day scope names
// several routes (diff, tree, cost, gates, events, runners, flows) this
// pass does not build (see L20-webui.md's Future work) — so an empty
// WebPath means "no web route yet," not a parity failure the way a
// missing CLIVerb is.
type Op struct {
	Method  string
	Path    string
	CLIVerb string
	WebPath string
}

// Ops is every operation L04 registers. Adding an operation later
// documents adds an entry here; the parity test in internal/archtest
// fails until both the route and the verb exist.
var Ops = []Op{
	{Method: "POST", Path: "/runs", CLIVerb: "run", WebPath: "POST /runs"},
	{Method: "POST", Path: "/do", CLIVerb: "do", WebPath: "POST /chat"},
	{Method: "GET", Path: "/runs", CLIVerb: "ls", WebPath: "GET /runs"},
	{Method: "GET", Path: "/runs/{id}", CLIVerb: "show", WebPath: "GET /runs/{id}"},
	{Method: "GET", Path: "/runs/{id}/conversation", CLIVerb: "conversation show", WebPath: "GET /c/{runID}"},
	{Method: "GET", Path: "/events", CLIVerb: "logs", WebPath: "GET /events"},
	{Method: "POST", Path: "/runs/{id}/conversation/messages", CLIVerb: "conversation send", WebPath: "POST /c/{runID}/messages"},
	{Method: "POST", Path: "/runs/{id}/approve", CLIVerb: "approve", WebPath: "POST /t/{runID}/{nodeID}/answer"},
	{Method: "GET", Path: "/runs/{id}/effects", CLIVerb: "effects"},
	{Method: "POST", Path: "/runs/{id}/effects/resolve", CLIVerb: "effects resolve"},
	{Method: "POST", Path: "/runs/{id}/waivers", CLIVerb: "waiver grant"},
	{Method: "POST", Path: "/runs/{id}/effects/confirm", CLIVerb: "effects confirm"},
	{Method: "GET", Path: "/human-tasks", CLIVerb: "human-tasks", WebPath: "GET /"},
	{Method: "POST", Path: "/runs/{id}/fork", CLIVerb: "fork", WebPath: "POST /runs/{id}/fork"},
	{Method: "POST", Path: "/runs/{id}/cancel", CLIVerb: "cancel", WebPath: "POST /runs/{id}/cancel"},
	{Method: "GET", Path: "/runs/{a}/compare/{b}", CLIVerb: "compare", WebPath: "GET /compare"},
	{Method: "GET", Path: "/runs/{id}/diff", CLIVerb: "diff", WebPath: "GET /runs/{id}/diff"},
	{Method: "GET", Path: "/status", CLIVerb: "status"},
	{Method: "GET", Path: "/doctor", CLIVerb: "doctor", WebPath: "GET /doctor"},
	{Method: "POST", Path: "/db/verify", CLIVerb: "db verify"},
	{Method: "POST", Path: "/db/rebuild", CLIVerb: "db reindex"},
	{Method: "POST", Path: "/sources", CLIVerb: "src add"},
	{Method: "GET", Path: "/sources", CLIVerb: "src ls", WebPath: "GET /sources"},
	{Method: "POST", Path: "/sources/{id}/pause", CLIVerb: "src pause", WebPath: "POST /sources/{id}/pause"},
	{Method: "POST", Path: "/sources/{id}/resume", CLIVerb: "src resume"},
	{Method: "POST", Path: "/pause", CLIVerb: "pause"},
	{Method: "POST", Path: "/pause", CLIVerb: "park"},
	{Method: "POST", Path: "/resume", CLIVerb: "resume"},
	{Method: "POST", Path: "/selfcheck", CLIVerb: "doctor"},
	{Method: "POST", Path: "/db/backup", CLIVerb: "db backup"},
	{Method: "GET", Path: "/cost", CLIVerb: "cost", WebPath: "GET /cost"},
	{Method: "POST", Path: "/projects", CLIVerb: "project create", WebPath: "POST /projects"},
	{Method: "GET", Path: "/projects", CLIVerb: "project ls", WebPath: "GET /projects"},
	{Method: "POST", Path: "/sessions", CLIVerb: "session start", WebPath: "POST /sessions"},
	{Method: "GET", Path: "/sessions", CLIVerb: "session ls", WebPath: "GET /sessions"},
	{Method: "GET", Path: "/sessions/{id}", CLIVerb: "session show", WebPath: "GET /sessions/{id}"},
	{Method: "DELETE", Path: "/sessions/{id}", CLIVerb: "session end", WebPath: "DELETE /sessions/{id}"},
	{Method: "GET", Path: "/fs/browse", CLIVerb: "fs browse", WebPath: "GET /frag/fs/browse"},
}
