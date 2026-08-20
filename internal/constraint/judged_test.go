package constraint_test

import (
	"context"
	"testing"

	"github.com/williamokano/kairos/internal/admission"
	"github.com/williamokano/kairos/internal/constraint"
	"github.com/williamokano/kairos/internal/executor/local"
	"github.com/williamokano/kairos/internal/registry"
)

// fakeJudge returns canned verdicts by actor name, in call order per
// actor — enough to script "both agree", "one refutes", and
// "no evidence" scenarios without a real LLM CLI.
type fakeJudge struct {
	verdicts map[string]constraint.JudgeVerdict
}

func (f *fakeJudge) Judge(_ context.Context, req constraint.JudgeRequest) (constraint.JudgeVerdict, error) {
	return f.verdicts[req.Actor], nil
}

func newJudgedEvaluator(judge constraint.Judge) *constraint.Evaluator {
	return constraint.New(local.New(local.DefaultBootIDProvider()), admission.New(admission.Config{})).WithJudge(judge)
}

func TestEvaluate_judgedPassesWhenQuorumOfJudgesAgree(t *testing.T) {
	judge := &fakeJudge{verdicts: map[string]constraint.JudgeVerdict{
		"reviewer-a": {Refuted: false, Evidence: []string{"looked fine"}},
		"reviewer-b": {Refuted: false, Evidence: []string{"also fine"}},
	}}
	e := newJudgedEvaluator(judge)
	result, err := e.Evaluate(context.Background(), constraint.Input{
		Gate: registry.GateDef{
			ID: "security-review", Kind: registry.GateJudged, JudgeFraming: "refutation",
			JudgeActors: []string{"reviewer-a", "reviewer-b"}, JudgeQuorumOf: 2,
		},
		Output: map[string]any{},
		Dir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !result.Passed {
		t.Fatalf("Passed = false, want true; Reason=%q", result.Reason)
	}
}

func TestEvaluate_judgedFailsWhenOneJudgeRefutesWithEvidence(t *testing.T) {
	judge := &fakeJudge{verdicts: map[string]constraint.JudgeVerdict{
		"reviewer-a": {Refuted: true, Evidence: []string{"found unescaped SQL concatenation at line 42"}},
		"reviewer-b": {Refuted: false, Evidence: []string{"looked fine"}},
	}}
	e := newJudgedEvaluator(judge)
	result, err := e.Evaluate(context.Background(), constraint.Input{
		Gate: registry.GateDef{
			ID: "security-review", Kind: registry.GateJudged, JudgeFraming: "refutation",
			JudgeActors: []string{"reviewer-a", "reviewer-b"}, JudgeQuorumOf: 2,
		},
		Output: map[string]any{},
		Dir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if result.Passed {
		t.Fatal("Passed = true, want false — a single well-evidenced refutation must fail the gate")
	}
	if len(result.Findings) == 0 {
		t.Fatal("expected findings citing the refutation's evidence")
	}
}

func TestEvaluate_judgedVerdictWithNoEvidenceIsInconclusiveNotAPass(t *testing.T) {
	judge := &fakeJudge{verdicts: map[string]constraint.JudgeVerdict{
		"reviewer-a": {Refuted: false, Evidence: nil}, // no evidence — must not count toward quorum
		"reviewer-b": {Refuted: false, Evidence: []string{"fine"}},
	}}
	e := newJudgedEvaluator(judge)
	result, err := e.Evaluate(context.Background(), constraint.Input{
		Gate: registry.GateDef{
			ID: "security-review", Kind: registry.GateJudged, JudgeFraming: "refutation",
			JudgeActors: []string{"reviewer-a", "reviewer-b"}, JudgeQuorumOf: 2,
		},
		Output: map[string]any{},
		Dir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if result.Passed {
		t.Fatal("Passed = true, want false — only 1/2 judges gave an evidence-backed non-refuting verdict, quorum is 2")
	}
}

func TestEvaluate_judgedWithNoConfiguredJudgeFailsLoudly(t *testing.T) {
	e := constraint.New(local.New(local.DefaultBootIDProvider()), admission.New(admission.Config{})) // no WithJudge
	result, err := e.Evaluate(context.Background(), constraint.Input{
		Gate: registry.GateDef{
			ID: "security-review", Kind: registry.GateJudged, JudgeFraming: "refutation",
			JudgeActors: []string{"reviewer-a"}, JudgeQuorumOf: 1,
		},
		Output: map[string]any{},
		Dir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if result.Passed {
		t.Fatal("Passed = true, want false — no judge is configured, this must fail loudly, not silently pass")
	}
}
