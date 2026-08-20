// Package constraint implements 05-gates.md's phase-0 slice of the gate
// library: the expr and command kinds. It never appends to the event
// log itself — internal/engine owns that — Evaluate returns a Result the
// caller turns into domain.ConstraintEvaluated (per gate) and, once every
// declared gate has run, the aggregate domain.NodeGatesEvaluated.
//
// The other eight kinds 05-gates.md names (file, regex, git-diff,
// coverage, judged, plus the three non-code domain kinds) are Future
// work — see L10-constraints-gates.md.
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

// Evaluator runs one gate's check. Safe for concurrent use: it holds only
// its two dependencies, no mutable state of its own.
type Evaluator struct {
	exec  local.Executor
	admit *admission.Manager
}

// New constructs an Evaluator. admit may be nil, in which case command
// gates skip the cpu.heavy permit request entirely (used by tests that
// don't need admission wired) — expr gates never touch it either way.
func New(exec local.Executor, admit *admission.Manager) *Evaluator {
	return &Evaluator{exec: exec, admit: admit}
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
