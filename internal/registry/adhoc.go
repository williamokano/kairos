package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/oklog/ulid/v2"
	"sigs.k8s.io/yaml"
)

// AdHocOptions parameterizes SynthesizeAdHoc. Actor is required (the
// caller — internal/api's `/do` handler — resolves the configured
// default, since registry itself has no config knowledge). ResumeSessionID
// and ConversationRunOverride back a chat's second-and-later turn; both
// empty means "a fresh session, posting into my own run" — turn one.
type AdHocOptions struct {
	Actor                   string
	ResumeSessionID         string
	ConversationRunOverride string
	// WorkDir, when non-empty, becomes the synthesized node's
	// WorkDirOverride — a `kairos session`'s real directory (internal/
	// project). Empty means the normal scratch dir (today's behaviour).
	WorkDir string
}

// SynthesizeAdHoc builds `kairos do`'s ad hoc single-node workflow — the
// concrete answer to the gap 09-cli-and-tui.md/L15-tui.md named and left
// unbuilt ("kairos do... needs a daemon-side endpoint accepting free
// text"). It is a real, one-node Definition like any hand-authored
// workflow: same Parse/ApplyDefaults/Validate path (LoadBytes), same
// publish-time checks, no bypass.
//
// Why a file on disk, not an in-memory Definition: TriggerReceived
// carries a DefinitionRef the engine re-resolves on every dispatch
// (engine.go's firstEventDefinitionRef + registry.Load) — for retries,
// crash recovery, and (L18) forking to work at all, the definition must
// still be readable by that same path arbitrarily long after this call
// returns. Written under $KAIROS_HOME/adhoc/<ulid>.yaml — never cleaned
// up by this function (no GC exists for this directory yet; see
// L24-kairos-do.md's Future work).
//
// The single node: actor = opts.Actor (any real actor kind this engine
// already dispatches — this function has no claude-specific knowledge),
// prompt = text verbatim, workspace: read (no git clone for a quick ad
// hoc task — see engine.Config.WorkspaceRepo's existing "empty means no
// write-mode node can run" constraint, which a chat task should never
// need to satisfy), output {result: "string!"} (a deliberately permissive
// single-string schema — an ad hoc task has no author to declare a
// richer contract, and result is also extractReplyText's first-checked
// key in internal/engine/actor_llm.go). postOutputToConversation: true,
// so the real output becomes a chat reply automatically (see
// NodeDef.PostOutputToConversation's doc comment).
func SynthesizeAdHoc(homeDir, text string, opts AdHocOptions) (path string, err error) {
	if opts.Actor == "" {
		return "", fmt.Errorf("registry: SynthesizeAdHoc requires a non-empty Actor")
	}

	node := map[string]any{
		"id":                       "task",
		"actor":                    opts.Actor,
		"prompt":                   text,
		"workspace":                string(WorkspaceRead),
		"output":                   map[string]any{"result": "string!"},
		"postOutputToConversation": true,
	}
	if opts.ResumeSessionID != "" {
		node["resumeSessionId"] = opts.ResumeSessionID
	}
	if opts.ConversationRunOverride != "" {
		node["conversationRunOverride"] = opts.ConversationRunOverride
	}
	if opts.WorkDir != "" {
		node["workDirOverride"] = opts.WorkDir
	}

	doc := map[string]any{
		"name":  "adhoc-" + ulid.Make().String(),
		"nodes": []map[string]any{node},
	}
	data, err := yaml.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("registry: marshalling ad hoc definition: %w", err)
	}

	// Validated BEFORE it's written anywhere durable — a synthesis bug
	// must fail this call loudly, never produce a file a later dispatch
	// discovers is broken (AGENTS.md rule 1).
	if _, err := LoadBytes(data, "<adhoc>"); err != nil {
		return "", fmt.Errorf("registry: synthesized ad hoc definition is invalid: %w", err)
	}

	dir := filepath.Join(homeDir, "adhoc")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("registry: creating adhoc dir: %w", err)
	}
	outPath := filepath.Join(dir, ulid.Make().String()+".yaml")
	if err := os.WriteFile(outPath, data, 0o600); err != nil {
		return "", fmt.Errorf("registry: writing ad hoc definition: %w", err)
	}
	return outPath, nil
}

// GC removes adhoc definition files under homeDir/adhoc that are BOTH not
// referenced by activeDefRefs (mirroring internal/workspace.GC's
// activeRunIDs pattern exactly — a keep-set the caller derives from
// non-terminal runs' own DefinitionRef, not a name this function invents
// itself) AND older than retention. The age check is the second,
// independent safety net raw activity-checking alone doesn't give: a
// TERMINAL run's adhoc file must stay forkable (L18) for some real window
// after it finishes, not just for as long as it happened to still be
// running — deleting it the instant a run succeeds would be correct by
// "not referenced by an active run" alone but would break forking minutes
// later. Never wired to a timer (no persisted timer wheel exists yet,
// matching L05-engine.md's decision #6 / internal/workspace.GC's own
// precedent) — a caller invokes this explicitly.
func GC(homeDir string, activeDefRefs map[string]bool, retention time.Duration, now time.Time) ([]string, error) {
	dir := filepath.Join(homeDir, "adhoc")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("registry: reading adhoc dir: %w", err)
	}
	var removed []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		full := filepath.Join(dir, e.Name())
		if activeDefRefs[full] {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return removed, fmt.Errorf("registry: stat-ing %s: %w", full, err)
		}
		if now.Sub(info.ModTime()) < retention {
			continue
		}
		if err := os.Remove(full); err != nil {
			return removed, fmt.Errorf("registry: removing %s: %w", full, err)
		}
		removed = append(removed, full)
	}
	return removed, nil
}
