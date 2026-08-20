// Package effect implements 05-gates.md's builtin effect providers — the
// concrete external-mutation operations (git.push, gh.pr.create) a node
// declared `actor: effect` performs. Every provider spawns its real
// subprocess through internal/executor/local, the sole execution
// chokepoint (AGENTS.md §2) — this package never imports os/exec.
package effect

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// pathEnv builds the Env slice git.go/gh.go's providers spawn their
// subprocess with — home points at the invocation's own scratch dir
// (matching every other spawn site in this codebase), and pathPrefix, if
// set, is tried before the fixed system PATH (also reflected in
// resolveBinary below, since internal/executor/local.Start resolves
// Argv[0] via the real os/exec LookPath against the CALLING PROCESS's
// ambient PATH, not spec.Env — setting Env alone does not redirect where
// the binary is found).
func pathEnv(home, pathPrefix string) []string {
	path := "/usr/bin:/bin:/usr/local/bin"
	if pathPrefix != "" {
		path = pathPrefix + ":" + path
	}
	return []string{"HOME=" + home, "PATH=" + path}
}

// binaryDirs are the fixed directories resolveBinary searches after
// pathPrefix — the same fixed set pathEnv advertises via PATH, kept in
// sync deliberately (this document never reads the daemon's own ambient
// PATH, matching AGENTS §5's "an integration test that reads the ambient
// PATH is a flaky test" for the daemon's real runtime behaviour too, not
// only its tests).
var binaryDirs = []string{"/usr/bin", "/bin", "/usr/local/bin"}

// resolveBinary finds name (e.g. "git", "gh") in pathPrefix first, then
// in binaryDirs — never via the process's ambient PATH, which
// os/exec.Command would otherwise silently consult regardless of the
// spawned child's own Env.
func resolveBinary(name, pathPrefix string) (string, error) {
	dirs := binaryDirs
	if pathPrefix != "" {
		dirs = append([]string{pathPrefix}, binaryDirs...)
	}
	for _, dir := range dirs {
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("effect: %q not found in %v", name, dirs)
}

// Outcome is the terminal state a Provider resolves an attempt or a probe
// to.
type Outcome string

const (
	Applied Outcome = "applied"
	Failed  Outcome = "failed"
)

// Request is one effect invocation.
type Request struct {
	RunID, NodeID, ExecID string
	// Effect is the builtin's own name, e.g. "git.push" — matches
	// Provider.Kind().
	Effect string
	// IdempotencyKey makes Attempt/Probe/Compensate safe to call more
	// than once for the same logical effect. See IdempotencyKey below;
	// the "runID" it's derived from is the lineage root
	// (internal/engine's lineageRootFor, L18) — a forked run's own id
	// until L18, now correctly the original ancestor's id, so a fork's
	// effect actions update the lineage's external state rather than
	// duplicating it.
	IdempotencyKey string
	// WorkDir is the git workspace (internal/workspace, L06) the effect
	// operates in.
	WorkDir string
	// Dir is the scratch directory a real provider spawns its subprocess
	// with (stdout.log/stderr.log/proc.json land here, per
	// internal/executor/local's existing convention) — distinct from
	// WorkDir exactly as actor_shell.go's Dir/WorkDir already are.
	Dir string
	// Args are the effect's static, author-declared parameters (a
	// node's `with:` block — registry.NodeDef.With). Dynamic
	// input-binding into Args is Future work; see L12's Documented
	// decisions.
	Args map[string]string
	// PathPrefix, when non-empty, is prepended to the child's PATH —
	// tests use this to point git.go/gh.go's providers at a fake `gh`
	// binary stub without touching the ambient PATH (AGENTS §5: "an
	// integration test that reads the ambient PATH is a flaky test").
	PathPrefix string
}

// Result is what Attempt/Probe resolve a Request to.
type Result struct {
	Outcome     Outcome
	ExternalRef string // the provider's own identifier for what it did — a PR URL, a pushed ref
	Reason      string // populated when Outcome == Failed
}

// Provider performs one class of builtin effect.
type Provider interface {
	// Kind is the effect name this provider answers for, e.g. "git.push".
	Kind() string
	// Attempt performs req's mutation. Called at most once per
	// IdempotencyKey on the ordinary path — a retry after a crash goes
	// through Probe instead (06-durability.md: "probe by idempotency
	// key, never blindly retry").
	Attempt(ctx context.Context, req Request) (Result, error)
	// Probe re-derives an already-attempted effect's outcome without
	// performing it again. ok=false means the provider found no
	// evidence either way — the caller must record effect.unknown,
	// never guess applied or failed.
	Probe(ctx context.Context, req Request) (result Result, ok bool, err error)
	// Compensate reverses an applied effect's external state, given the
	// ExternalRef Attempt/Probe returned. Returns ErrNotCompensable when
	// this effect has no declared reversal.
	Compensate(ctx context.Context, req Request, externalRef string) error
}

// ErrNotCompensable is Compensate's answer for an effect with no
// declared reversal (05-gates.md's "revert" line is per-effect, not
// universal — git.push has none in this document's scope).
var ErrNotCompensable = notCompensableError{}

type notCompensableError struct{}

func (notCompensableError) Error() string { return "effect: not compensable" }

// IdempotencyKey derives a stable key for (runID, nodeID, effect) — the
// same three inputs identify the same logical effect across every retry
// and every crash-and-recover cycle within one run's lineage. Pure and
// deterministic, matching AGENTS §4 rule 4 (domain determinism); it lives
// here rather than internal/domain because it is an engine/effect-layer
// concern, not a state-machine transition.
func IdempotencyKey(runID, nodeID, effect string) string {
	sum := sha256.Sum256([]byte(runID + "|" + nodeID + "|" + effect))
	return hex.EncodeToString(sum[:])[:16]
}
