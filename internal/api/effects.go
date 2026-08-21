package api

import (
	"encoding/json"
	"net/http"
)

func registerEffectsRoutes(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("GET /runs/{id}/effects", handleListEffects(deps))
	mux.HandleFunc("POST /runs/{id}/effects/resolve", handleResolveEffect(deps))
}

type effectSummaryResponse struct {
	NodeID                  string `json:"nodeId"`
	ExecID                  string `json:"execId"`
	Effect                  string `json:"effect"`
	Outcome                 string `json:"outcome"`
	ExternalRef             string `json:"externalRef,omitempty"`
	Reason                  string `json:"reason,omitempty"`
	Compensated             bool   `json:"compensated"`
	WouldCompensateOnCancel bool   `json:"wouldCompensateOnCancel"`
}

// handleListEffects is the daemon side of `kairos effects <run>`
// (L12-effects-compensation.md's own Future work: "the daemon-side data
// exists in the event log; only the CLI verb itself is unbuilt").
// Read-only — it never invokes a provider, only replays what's already
// recorded.
func handleListEffects(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID := r.PathValue("id")
		if deps.Engine == nil {
			writeError(w, http.StatusServiceUnavailable, "invariant_violation", "engine not configured")
			return
		}
		summaries, err := deps.Engine.Effects(r.Context(), runID)
		if err != nil {
			writeError(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		out := make([]effectSummaryResponse, len(summaries))
		for i, s := range summaries {
			out[i] = effectSummaryResponse{
				NodeID: s.NodeID, ExecID: s.ExecID, Effect: s.Effect, Outcome: s.Outcome,
				ExternalRef: s.ExternalRef, Reason: s.Reason,
				Compensated: s.Compensated, WouldCompensateOnCancel: s.WouldCompensateOnCancel,
			}
		}
		writeJSON(w, http.StatusOK, out)
	}
}

type resolveEffectRequest struct {
	NodeID  string `json:"nodeId"`
	Outcome string `json:"outcome"` // "applied" | "failed"
	Reason  string `json:"reason"`
}

// handleResolveEffect is the daemon side of `kairos effects resolve` —
// the CLI-reachable form of engine.ResolveEffectUnknown, previously
// callable only by extending recoverLost's own internal pattern
// (L12-effects-compensation.md's own Future work: "an operator cannot
// yet resolve a blocked effect.unknown node without direct event-store
// access").
func handleResolveEffect(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID := r.PathValue("id")
		var req resolveEffectRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "usage", "invalid request body: "+err.Error())
			return
		}
		if req.NodeID == "" || req.Outcome == "" || req.Reason == "" {
			writeError(w, http.StatusBadRequest, "usage", "nodeId, outcome, and reason are all required")
			return
		}
		if deps.Engine == nil {
			writeError(w, http.StatusServiceUnavailable, "invariant_violation", "engine not configured")
			return
		}
		if err := deps.Engine.ResolveEffectUnknown(r.Context(), runID, req.NodeID, req.Outcome, req.Reason); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invariant_violation", err.Error())
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}
