package constraint

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/williamokano/kairos/internal/domain"
)

// evaluateJudged implements 05-gates.md's "judged" kind: an actor's
// opinion, framed as refutation ("try to find a violation; default to
// inconclusive"), with evidence required for any verdict to count.
// Quorum: JudgeQuorumOf of len(JudgeActors) judges must agree the claim
// is NOT refuted, each with evidence, for the gate to pass. A judge whose
// verdict lacks evidence is treated as inconclusive — it counts toward
// neither a pass nor (necessarily) a fail; only an explicit,
// evidence-backed Refuted=true actively fails the gate outright
// (05-gates.md's "unbluffable" framing: a single well-evidenced
// refutation is disqualifying, matching real review — one reviewer
// finding a real SQL injection outweighs two who didn't look hard
// enough).
func (e *Evaluator) evaluateJudged(ctx context.Context, in Input) (Result, error) {
	start := time.Now()
	if e.judge == nil {
		return e.gitFailure(in, start, "judged gate kind requires a configured judge, none provided (see internal/engine's Judge wiring)"), nil
	}

	gd := in.Gate
	var agreeing int
	var findings []domain.Finding
	for i, actor := range gd.JudgeActors {
		req := JudgeRequest{Actor: actor, Lens: gd.JudgeLens, Output: in.Output, Dir: filepath.Join(in.Dir, fmt.Sprintf("judge-%d", i))}
		verdict, err := e.judge.Judge(ctx, req)
		if err != nil {
			findings = append(findings, domain.Finding{ID: gd.ID, Message: fmt.Sprintf("judge %q failed: %v", actor, err), Severity: severityOrDefault(gd)})
			continue
		}
		if len(verdict.Evidence) == 0 {
			// Evidence required, or the verdict is inconclusive — and
			// inconclusive does not pass. Neither does it actively fail:
			// it simply does not count toward quorum.
			findings = append(findings, domain.Finding{ID: gd.ID, Message: fmt.Sprintf("judge %q returned no evidence; treated as inconclusive", actor), Severity: "low"})
			continue
		}
		if verdict.Refuted {
			for _, ev := range verdict.Evidence {
				findings = append(findings, domain.Finding{ID: gd.ID, Message: fmt.Sprintf("judge %q refuted the claim: %s", actor, ev), Severity: severityOrDefault(gd)})
			}
			continue
		}
		agreeing++
	}

	dur := msToDuration(start)
	if agreeing >= gd.JudgeQuorumOf {
		return Result{Passed: true, Reason: fmt.Sprintf("%d/%d judges agreed (quorum %d)", agreeing, len(gd.JudgeActors), gd.JudgeQuorumOf), DurationMs: dur}, nil
	}
	if len(findings) == 0 {
		findings = []domain.Finding{{ID: gd.ID, Message: fmt.Sprintf("only %d/%d judges agreed, quorum %d not met", agreeing, len(gd.JudgeActors), gd.JudgeQuorumOf), Severity: severityOrDefault(gd)}}
	}
	msg := gd.Message
	if msg == "" {
		msg = fmt.Sprintf("judged gate did not reach quorum (%d/%d, need %d)", agreeing, len(gd.JudgeActors), gd.JudgeQuorumOf)
	}
	return Result{Passed: false, Reason: msg, DurationMs: dur, Findings: findings}, nil
}
