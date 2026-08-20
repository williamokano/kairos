package constraint

import (
	"fmt"
	"time"

	"github.com/expr-lang/expr"

	"github.com/williamokano/kairos/internal/domain"
)

// evaluateExpr runs an expr-lang/expr expression (ADR 0013) against the
// node's decoded output. In-process, free, unbluffable — 05-gates.md's
// highest-leverage gate kind. A malformed expression or one that
// references a field the output doesn't have fails safely as a normal
// Result{Passed: false}, never a panic (AGENTS §4 rule 1) — expr-lang's
// own evaluator does not panic on a missing map key (it evaluates to
// nil), but the defer/recover below is a second line of defence against
// any expression shape that does.
func (e *Evaluator) evaluateExpr(in Input) (result Result) {
	start := time.Now()
	defer func() {
		if r := recover(); r != nil {
			result = Result{
				Passed:     false,
				Reason:     fmt.Sprintf("gate %q: evaluator panicked: %v", in.Gate.ID, r),
				DurationMs: msToDuration(start),
				Findings: []domain.Finding{{
					ID: in.Gate.ID, Message: fmt.Sprintf("expression evaluation panicked: %v", r), Severity: severityOrDefault(in.Gate),
				}},
			}
		}
	}()

	env := map[string]any{"output": in.Output}
	program, err := expr.Compile(in.Gate.Expr, expr.Env(env), expr.AsBool())
	if err != nil {
		return e.exprFailure(in, start, fmt.Sprintf("compiling expression: %v", err))
	}

	out, err := expr.Run(program, env)
	if err != nil {
		return e.exprFailure(in, start, fmt.Sprintf("evaluating expression: %v", err))
	}

	passed, ok := out.(bool)
	if !ok {
		return e.exprFailure(in, start, fmt.Sprintf("expression did not evaluate to a boolean (got %T)", out))
	}

	dur := msToDuration(start)
	if passed {
		return Result{Passed: true, Reason: "expression evaluated true", DurationMs: dur}
	}

	msg := in.Gate.Message
	if msg == "" {
		msg = fmt.Sprintf("gate %q: expression evaluated false", in.Gate.ID)
	}
	return Result{
		Passed: false, Reason: msg, DurationMs: dur,
		Findings: []domain.Finding{{ID: in.Gate.ID, Message: msg, Severity: severityOrDefault(in.Gate)}},
	}
}

func (e *Evaluator) exprFailure(in Input, start time.Time, reason string) Result {
	msg := fmt.Sprintf("gate %q: %s", in.Gate.ID, reason)
	return Result{
		Passed: false, Reason: msg, DurationMs: msToDuration(start),
		Findings: []domain.Finding{{ID: in.Gate.ID, Message: msg, Severity: severityOrDefault(in.Gate)}},
	}
}
