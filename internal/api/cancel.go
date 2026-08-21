package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/williamokano/kairos/internal/engine"
)

func registerCancelRoutes(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("POST /runs/{id}/cancel", handleCancel(deps))
}

type cancelRequest struct {
	Reason string `json:"reason"`
}

// handleCancel is the daemon side of `kairos cancel` (09-cli-and-tui.md) —
// L20-webui.md's Future work named this alongside fork/say/source-pause as
// already having "the CLI verbs and daemon routes all exist"; for cancel
// that was not true anywhere in this tree (no Engine.Cancel, no route, no
// CLI verb) until this pass built engine.Engine.Cancel (see cancel.go's own
// doc comment for what was and wasn't already real). Compensation of
// applied effects, if any, runs automatically and unconditionally —
// shard.go's existing Running/Degraded -> Cancelled transition handler,
// unaffected by this document.
func handleCancel(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID := r.PathValue("id")
		var req cancelRequest
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
		err := deps.Engine.Cancel(r.Context(), runID, req.Reason)
		switch {
		case err == nil:
			writeJSON(w, http.StatusOK, map[string]bool{"cancelled": true})
		case errors.Is(err, engine.ErrCancelReasonRequired):
			writeError(w, http.StatusUnprocessableEntity, "validation_failed", err.Error())
		case errors.Is(err, engine.ErrRunAlreadyTerminal), errors.Is(err, engine.ErrRunNotCancellable):
			writeError(w, http.StatusConflict, "invalid_state", err.Error())
		default:
			writeError(w, http.StatusNotFound, "not_found", err.Error())
		}
	}
}
