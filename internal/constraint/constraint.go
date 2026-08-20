// Package constraint implements 05-gates.md's gate library: expr and
// command (L10), plus file, regex, git-diff, coverage, and judged (L11).
// It never appends to the event log itself — internal/engine owns that —
// Evaluate returns a Result the caller turns into
// domain.ConstraintEvaluated (per gate) and, once every declared gate has
// run, the aggregate domain.NodeGatesEvaluated.
//
// The three non-code domain kinds 05-gates.md names (grounded,
// recipients, outbound-scan) are Future work — see 13-domains.md and
// L11-policy-secrets.md.
package constraint

import (
	"context"
	"fmt"
	"time"

	"github.com/williamokano/kairos/internal/admission"
	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/executor/local"
	"github.com/williamokano/kairos/internal/registry"
)

// maxEvidenceBytes caps how much of a command gate's captured stdout is
// kept as inline evidence in a Result's Reason — 05-gates.md's "64 KiB
// tail as evidence" clause. Storing the full output as an artifact (the
// clause's other half) is Future work: internal/artifact exists (L09)
// but wiring gate evidence through it is not this document's scope — see
// L10-constraints-gates.md's Documented decisions.
const maxEvidenceBytes = 64 * 1024

// Judge invokes one judged-gate actor and returns its verdict. Defined
// here (not imported from internal/engine) to avoid a circular import:
// internal/engine already imports internal/constraint to dispatch gate
// evaluation, so the direction cannot reverse. internal/engine implements
// this interface (actor_judge.go) by spawning the configured judge
// binary through internal/executor/local — the same file-contract
// machinery L08's llm actor uses, reused rather than duplicated.
type Judge interface {
	Judge(ctx context.Context, req JudgeRequest) (JudgeVerdict, error)
}

// JudgeRequest is one judged-gate actor invocation's parameters.
type JudgeRequest struct {
	Actor  string
	Lens   string
	Output map[string]any // the node's typed output the judge evaluates
	Dir    string         // scratch dir for this one judge invocation
}

// JudgeVerdict is one judge's answer. Evidence is required for a verdict
// to count as anything other than inconclusive — 05-gates.md: "evidence
// required, or the verdict is inconclusive, and inconclusive does not
// pass."
type JudgeVerdict struct {
	Refuted  bool     // true: the judge found a violation (framing: refutation)
	Evidence []string // must be non-empty for Refuted==false to count as a pass
}

// Evaluator runs one gate's check. Safe for concurrent use: it holds only
// its dependencies, no mutable state of its own.
type Evaluator struct {
	exec  local.Executor
	admit *admission.Manager
	judge Judge
}

// New constructs an Evaluator. admit may be nil, in which case command
// gates skip the cpu.heavy permit request entirely (used by tests that
// don't need admission wired) — expr gates never touch it either way.
// judge may be nil, in which case a judged gate fails loudly rather than
// silently passing (AGENTS §4 rule 1) — WithJudge attaches one.
func New(exec local.Executor, admit *admission.Manager) *Evaluator {
	return &Evaluator{exec: exec, admit: admit}
}

// WithJudge returns a copy of e with judge attached, for callers (the
// engine) that have a real Judge implementation to wire in.
func (e *Evaluator) WithJudge(judge Judge) *Evaluator {
	cp := *e
	cp.judge = judge
	return &cp
}

// Input is one gate evaluation's parameters.
type Input struct {
	Gate registry.GateDef

	RunID, NodeID, ExecID string
	// Output is the node's own typed JSON output, already decoded — what
	// an expr gate's expression evaluates against.
	Output map[string]any
	// WorkDir is a command gate's cwd base: the node's provisioned
	// workspace, or its bare scratch dir for a workspace: none/read node.
	// Gate.Workdir, when set, is joined onto this (validate.go rejects an
	// absolute Gate.Workdir at publish time).
	WorkDir string
	// Dir is where this one evaluation's own stdout.log/stderr.log/
	// proc.json are written — a gate-scoped subdirectory of the node
	// exec's scratch dir, never reused across gates or attempts.
	Dir string
	// BaseRef is the run's base ref (05-gates.md's `{{ .base }}` — never
	// a hardcoded "origin/main"), consulted by regex (over: added-lines)
	// and git-diff kinds. Empty means "no base ref available" (the
	// workspace is not a git checkout, or none was configured) — those
	// kinds fail loudly with a clear reason rather than guessing one.
	BaseRef string
}

// Result is one gate's verdict — never itself an error return; a gate
// that fails its check is a normal Result{Passed: false}, not an error.
// Evaluate returns a non-nil error only for something the gate schedule
// itself cannot recover from (e.g. an unsupported Kind slipping past
// registry validation).
type Result struct {
	Passed     bool
	Reason     string
	ExitCode   int
	DurationMs int64
	Findings   []domain.Finding
}

// Evaluate runs in.Gate's check and returns its verdict.
func (e *Evaluator) Evaluate(ctx context.Context, in Input) (Result, error) {
	switch in.Gate.Kind {
	case registry.GateExpr:
		return e.evaluateExpr(in), nil
	case registry.GateCommand:
		return e.evaluateCommand(ctx, in)
	case registry.GateFile:
		return e.evaluateFile(in), nil
	case registry.GateRegex:
		return e.evaluateRegex(ctx, in)
	case registry.GateGitDiff:
		return e.evaluateGitDiff(ctx, in)
	case registry.GateCoverage:
		return e.evaluateCoverage(ctx, in)
	case registry.GateJudged:
		return e.evaluateJudged(ctx, in)
	default:
		return Result{}, fmt.Errorf("constraint: unsupported gate kind %q", in.Gate.Kind)
	}
}

func severityOrDefault(gd registry.GateDef) string {
	if gd.Severity != "" {
		return gd.Severity
	}
	return "high"
}

func capBytes(b []byte, limit int) []byte {
	if len(b) <= limit {
		return b
	}
	return b[len(b)-limit:]
}

func msToDuration(start time.Time) int64 {
	return time.Since(start).Milliseconds()
}
