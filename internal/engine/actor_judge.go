package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/williamokano/kairos/internal/constraint"
	"github.com/williamokano/kairos/internal/executor/local"
)

// Judge implements constraint.Judge, giving the judged gate kind (L11) a
// real actor invocation without internal/constraint importing
// internal/engine (which would be circular — engine already imports
// constraint to dispatch gate evaluation). A synchronous spawn-and-wait,
// deliberately NOT routed through dispatchLLMActor's CmdStartNode/
// admission/domain-event machinery: a judge invocation is gate-evaluation
// plumbing, not a NodeExecution in its own right, and does not appear in
// the run's own event stream as a node.
//
// Documented scope limit (see L11-policy-secrets.md's Documented
// decisions): every named judge actor is invoked via the SAME configured
// e.llmBinary (L08's single-binary knob — no per-actor CLI resolution
// exists yet). 05-gates.md's "quorum across two different CLIs" is
// therefore, today, quorum across two prompts/lenses on the same binary,
// not literal cross-CLI diversity. Real per-actor binary resolution is
// Future work.
func (e *Engine) Judge(ctx context.Context, req constraint.JudgeRequest) (constraint.JudgeVerdict, error) {
	if e.llmBinary == "" {
		return constraint.JudgeVerdict{}, fmt.Errorf("judged gate kind requires a configured LLM binary (engine.Config.LLMBinary is empty)")
	}
	if err := os.MkdirAll(req.Dir, 0o700); err != nil {
		return constraint.JudgeVerdict{}, fmt.Errorf("creating judge scratch dir: %w", err)
	}
	outputPath := filepath.Join(req.Dir, "verdict.json")

	outputJSON, err := json.Marshal(req.Output)
	if err != nil {
		return constraint.JudgeVerdict{}, fmt.Errorf("encoding output under judgement: %w", err)
	}
	prompt := fmt.Sprintf(
		"You are %q, a refutation judge. Lens: %s.\n"+
			"Try to find a concrete violation in the output below; default to inconclusive if you cannot find one — do not affirm, only refute or abstain.\n"+
			"Write your verdict as JSON to the file named by $KAIROS_OUTPUT: {\"refuted\": bool, \"evidence\": [\"...\"]}.\n"+
			"evidence must be non-empty and cite something concrete from the output; a verdict with no evidence is treated as inconclusive, not a pass.\n\n"+
			"Output under judgement:\n%s",
		req.Actor, req.Lens, string(outputJSON))

	started, err := e.exec.Start(ctx, local.ExecSpec{
		Dir:     req.Dir,
		WorkDir: req.Dir,
		Env: []string{
			"HOME=" + req.Dir,
			"PATH=/usr/bin:/bin:/usr/local/bin",
			"TERM=dumb", "NO_COLOR=1", "CI=1",
			"KAIROS_OUTPUT=" + outputPath,
		},
		Argv:  []string{e.llmBinary},
		Stdin: []byte(prompt),
	})
	if err != nil {
		return constraint.JudgeVerdict{}, fmt.Errorf("starting judge %q: %w", req.Actor, err)
	}
	res, err := e.exec.Wait(ctx, started.PID)
	if err != nil {
		return constraint.JudgeVerdict{}, fmt.Errorf("waiting for judge %q: %w", req.Actor, err)
	}
	if res.ExitCode != 0 {
		return constraint.JudgeVerdict{}, fmt.Errorf("judge %q exited %d", req.Actor, res.ExitCode)
	}

	body, err := os.ReadFile(outputPath)
	if err != nil {
		return constraint.JudgeVerdict{}, fmt.Errorf("judge %q wrote no verdict: %w", req.Actor, err)
	}
	var v struct {
		Refuted  bool     `json:"refuted"`
		Evidence []string `json:"evidence"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return constraint.JudgeVerdict{}, fmt.Errorf("judge %q wrote invalid JSON: %w", req.Actor, err)
	}
	return constraint.JudgeVerdict{Refuted: v.Refuted, Evidence: v.Evidence}, nil
}
