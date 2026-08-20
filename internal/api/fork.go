package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/williamokano/kairos/internal/engine"
)

func registerForkRoutes(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("POST /runs/{id}/fork", handleFork(deps))
}

type forkRequest struct {
	AtSequence int               `json:"atSequence,omitempty"`
	Overrides  map[string]string `json:"overrides,omitempty"`
	AllowDrift bool              `json:"allowDrift,omitempty"`
}

type forkResponse struct {
	NewRunID          string `json:"newRunId"`
	Drifted           bool   `json:"drifted"`
	ActualSnapshotSeq int    `json:"actualSnapshotSeq"`
}

// handleFork is the daemon side of `kairos fork` (06-durability.md).
// AllowDrift must be explicit in the request body — there is no
// implicit-yes default, matching every other irreversible-by-default
// confirmation this codebase requires spelled out (L13's --confirm,
// L13's --unattended acknowledgement string).
func handleFork(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID := r.PathValue("id")
		var req forkRequest
		if r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeError(w, http.StatusBadRequest, "usage", "invalid request body: "+err.Error())
				return
			}
		}
		if deps.Engine == nil {
			writeError(w, http.StatusServiceUnavailable, "invariant_violation", "engine not configured")
			return
		}

		result, err := deps.Engine.Fork(r.Context(), engine.ForkRequest{
			FromRunID: runID, AtSequence: req.AtSequence, Overrides: req.Overrides, AllowDrift: req.AllowDrift,
		})
		switch {
		case err == nil:
			writeJSON(w, http.StatusCreated, forkResponse{
				NewRunID: result.NewRunID, Drifted: result.Drifted, ActualSnapshotSeq: result.ActualSnapshotSeq,
			})
		case errors.Is(err, engine.ErrWorkspaceDrift):
			writeError(w, http.StatusConflict, "workspace_drift", err.Error())
		default:
			writeError(w, http.StatusUnprocessableEntity, "invariant_violation", err.Error())
		}
	}
}
