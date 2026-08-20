package archtest

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/williamokano/kairos/internal/apispec"
	"github.com/williamokano/kairos/internal/cli"
)

// cliVerbExists reports whether root's command tree contains a command
// reachable by exactly the space-joined verb string (e.g. "db verify").
// cobra's Find recurses through subcommands for a multi-token path; the
// CommandPath comparison guards against Find silently matching a shorter
// prefix and ignoring leftover, unmatched tokens.
func cliVerbExists(root *cobra.Command, verb string) bool {
	found, _, err := root.Find(splitVerb(verb))
	if err != nil {
		return false
	}
	return found.CommandPath() == root.Name()+" "+verb
}

func splitVerb(verb string) []string {
	return strings.Fields(verb)
}

// leafCommands returns every command in root's tree with no children, as
// space-joined verb paths relative to root (root's own name excluded).
func leafCommands(root *cobra.Command) []string {
	var out []string
	for _, sub := range root.Commands() {
		out = append(out, collectLeaves(sub, "")...)
	}
	return out
}

func collectLeaves(cmd *cobra.Command, prefix string) []string {
	path := cmd.Name()
	if prefix != "" {
		path = prefix + " " + path
	}
	if !cmd.HasSubCommands() {
		return []string{path}
	}
	var out []string
	for _, sub := range cmd.Commands() {
		out = append(out, collectLeaves(sub, path)...)
	}
	return out
}

// exemptCLIVerbs are leaf commands with no corresponding apispec.Op:
// version (pre-dates the daemon, no route) and serve (daemon lifecycle,
// not itself a route call — it's what BINDS the routes). check-output
// (L08) is a local file-validation tool an actor process runs inside its
// own workspace — it never talks to the daemon at all, so there is no
// route for it to map to. web (L20) mints a token URL and opens a
// browser — like serve, it is what BINDS the web UI's routes rather than
// itself being one.
var exemptCLIVerbs = map[string]bool{"version": true, "serve": true, "check-output": true, "web": true}

// TestUI_everyCallHasCLICounterpart (AGENTS.md §9) is real from L04:
// every apispec.Op maps to a CLI verb that reaches it, and every non-exempt
// CLI leaf verb is named by at least one Op — so neither surface can grow
// a private code path. Scoped to what L04 registers; a future web UI (L20)
// extends apispec.Op with a WebCall dimension rather than retrofitting
// this discipline from scratch.
func TestUI_everyCallHasCLICounterpart(t *testing.T) {
	if len(apispec.Ops) == 0 {
		t.Fatal("apispec.Ops is empty — this check would be vacuous")
	}

	root := cli.RootCommand()

	for _, op := range apispec.Ops {
		if !cliVerbExists(root, op.CLIVerb) {
			t.Errorf("%s %s -> CLI verb %q not found in the command tree", op.Method, op.Path, op.CLIVerb)
		}
	}

	opVerbs := map[string]bool{}
	for _, op := range apispec.Ops {
		opVerbs[op.CLIVerb] = true
	}
	for _, verb := range leafCommands(root) {
		if exemptCLIVerbs[verb] {
			continue
		}
		if !opVerbs[verb] {
			t.Errorf("CLI verb %q has no apispec.Op naming it — a private code path?", verb)
		}
	}
}
