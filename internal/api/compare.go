package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/williamokano/kairos/internal/domain"
)

func registerCompareRoutes(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("GET /runs/{a}/compare/{b}", handleCompare(deps))
}

// runSummaryForCompare is one side of `kairos compare`'s side-by-side —
// 12-build-plan.md: "kairos compare shows cost, duration, attempts, and
// findings side by side." Cost is not yet a durably recorded fact
// anywhere in the event log (L07's admission checks an ESTIMATE, never
// meters actual spend — NL-30) — reporting a real number here would be
// fabricating one, so it is omitted rather than faked; registered as
// Future work in L18-fork-replay-verify.md.
type runSummaryForCompare struct {
	RunID       string        `json:"runId"`
	Status      string        `json:"status"`
	Duration    time.Duration `json:"durationNanos"`
	Attempts    int           `json:"attempts"` // sum of every node's total NodeExecutionStarted count
	Findings    int           `json:"findings"` // sum of every node.gates.evaluated's Findings length
	ForkedFrom  string        `json:"forkedFrom,omitempty"`
	Drifted     bool          `json:"drifted"`
	DriftDetail string        `json:"driftDetail,omitempty"`
}

type compareResponse struct {
	A runSummaryForCompare `json:"a"`
	B runSummaryForCompare `json:"b"`
}

func handleCompare(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, err := summarizeForCompare(r.Context(), deps, r.PathValue("a"))
		if err != nil {
			writeError(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		b, err := summarizeForCompare(r.Context(), deps, r.PathValue("b"))
		if err != nil {
			writeError(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, compareResponse{A: a, B: b})
	}
}

func summarizeForCompare(ctx context.Context, deps Deps, runID string) (runSummaryForCompare, error) {
	envs, err := deps.Store.Read(ctx, runID)
	if err != nil {
		return runSummaryForCompare{}, err
	}
	if len(envs) == 0 {
		return runSummaryForCompare{}, fmt.Errorf("no such run: %s", runID)
	}

	out := runSummaryForCompare{RunID: runID}
	out.Duration = envs[len(envs)-1].OccurredAt.Sub(envs[0].OccurredAt)

	state, ok, err := deps.Store.GetRunState(ctx, runID)
	if err != nil {
		return runSummaryForCompare{}, err
	}
	if ok {
		out.Status = string(state.Status)
	}

	for _, env := range envs {
		switch ev := env.Event.(type) {
		case domain.NodeExecutionStarted:
			out.Attempts++
		case domain.NodeGatesEvaluated:
			out.Findings += len(ev.Findings)
		case domain.RunForked:
			out.ForkedFrom = ev.FromRunID
		case domain.ForkWorkspaceDrifted:
			out.Drifted = true
			out.DriftDetail = "requested sequence had no snapshot; restored from a later, live one instead"
		}
	}
	return out, nil
}
