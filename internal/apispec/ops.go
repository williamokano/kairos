// Package apispec is the neutral, zero-dependency source of truth mapping
// daemon API operations to CLI verbs. internal/api and internal/cli each
// consult it independently — neither imports the other — which is what
// keeps "nothing imports internal/api" true (internal/api stays a leaf,
// per AGENTS.md §2) while giving TestUI_everyCallHasCLICounterpart
// (AGENTS.md §9) something concrete to walk, real from L04 rather than
// retrofitted once a web UI (L20) exists.
package apispec

// Op is one daemon API operation and the CLI verb that exercises it.
type Op struct {
	Method  string
	Path    string
	CLIVerb string
}

// Ops is every operation L04 registers. Adding an operation later
// documents adds an entry here; the parity test in internal/archtest
// fails until both the route and the verb exist.
var Ops = []Op{
	{Method: "POST", Path: "/runs", CLIVerb: "run"},
	{Method: "GET", Path: "/runs", CLIVerb: "ls"},
	{Method: "GET", Path: "/runs/{id}", CLIVerb: "show"},
	{Method: "GET", Path: "/runs/{id}/conversation", CLIVerb: "conversation show"},
	{Method: "POST", Path: "/runs/{id}/conversation/messages", CLIVerb: "conversation send"},
	{Method: "POST", Path: "/runs/{id}/approve", CLIVerb: "approve"},
	{Method: "POST", Path: "/runs/{id}/fork", CLIVerb: "fork"},
	{Method: "GET", Path: "/runs/{a}/compare/{b}", CLIVerb: "compare"},
	{Method: "GET", Path: "/status", CLIVerb: "status"},
	{Method: "GET", Path: "/doctor", CLIVerb: "doctor"},
	{Method: "POST", Path: "/db/verify", CLIVerb: "db verify"},
	{Method: "POST", Path: "/db/rebuild", CLIVerb: "db reindex"},
	{Method: "POST", Path: "/sources", CLIVerb: "src add"},
	{Method: "GET", Path: "/sources", CLIVerb: "src ls"},
	{Method: "POST", Path: "/sources/{id}/pause", CLIVerb: "src pause"},
	{Method: "POST", Path: "/sources/{id}/resume", CLIVerb: "src resume"},
}
