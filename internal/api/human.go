package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/williamokano/kairos/internal/engine"
)

func registerHumanRoutes(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("POST /runs/{id}/approve", handleApprove(deps))
}

type approveRequest struct {
	NodeID    string `json:"nodeId"`
	Decision  string `json:"decision"`
	Reason    string `json:"reason"`
	TypedWord string `json:"typedWord,omitempty"`
}

// handleApprove is the daemon side of `kairos approve` — 09-cli-and-tui.md:
// "the CLI path exists but cannot rubber-stamp." There is deliberately no
// `--yes`/`--all` equivalent anywhere in this request shape: every field
// but TypedWord is required, and engine.AnswerHumanTask is the one and
// only path that can append human.task.answered — see its doc comment for
// why the confirmation tier can't be smuggled in here either.
func handleApprove(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID := r.PathValue("id")
		var req approveRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "usage", "invalid request body: "+err.Error())
			return
		}
		if req.NodeID == "" || req.Decision == "" {
			writeError(w, http.StatusBadRequest, "usage", "nodeId and decision are required")
			return
		}
		if deps.Engine == nil {
			writeError(w, http.StatusServiceUnavailable, "invariant_violation", "engine not configured")
			return
		}

		err := deps.Engine.AnswerHumanTask(r.Context(), runID, req.NodeID, engine.AnswerDecision{
			Decision: req.Decision, Reason: req.Reason, TypedWord: req.TypedWord,
		})
		switch {
		case err == nil:
			w.WriteHeader(http.StatusOK)
		case errors.Is(err, engine.ErrHumanDecisionReasonRequired), errors.Is(err, engine.ErrHumanDecisionTypedWordRequired):
			writeError(w, http.StatusBadRequest, "usage", err.Error())
		default:
			writeError(w, http.StatusUnprocessableEntity, "invariant_violation", err.Error())
		}
	}
}
