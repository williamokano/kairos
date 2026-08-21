package api

import (
	"encoding/json"
	"net/http"
	"time"
)

func registerWaiverRoutes(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("POST /runs/{id}/waivers", handleGrantWaiver(deps))
	mux.HandleFunc("POST /runs/{id}/effects/confirm", handleConfirmEffect(deps))
}

type grantWaiverRequest struct {
	NodeID string `json:"nodeId"`
	GateID string `json:"gateId"`
	Reason string `json:"reason"`
	// TTL is a Go duration string (e.g. "24h") — the waiver's ExpiresAt is
	// time.Now().Add(TTL). Required: an unexpiring waiver is exactly the
	// silent, permanent bypass 05-gates.md's waivable:false rule and this
	// whole feature exist to prevent from being the default.
	TTL string `json:"ttl"`
}

// handleGrantWaiver is the daemon side of `kairos waiver grant` —
// 05-gates.md's "waiver.grant is deny-tier for every non-human principal,"
// made reachable for the human operator it's FOR the first time
// (L11-policy-secrets.md's own Future work: engine.GrantWaiver already
// existed and enforced this, with no CLI/API route to call it). The
// actor is always "human" here — there is no field for the caller to
// supply a different one, since a request that reached this admin-facing
// socket at all came from a human operator's own terminal, exactly like
// kairos approve never asks the caller to assert who they are.
func handleGrantWaiver(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID := r.PathValue("id")
		var req grantWaiverRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "usage", "invalid request body: "+err.Error())
			return
		}
		if req.NodeID == "" || req.GateID == "" || req.Reason == "" || req.TTL == "" {
			writeError(w, http.StatusBadRequest, "usage", "nodeId, gateId, reason, and ttl are all required")
			return
		}
		ttl, err := time.ParseDuration(req.TTL)
		if err != nil {
			writeError(w, http.StatusBadRequest, "usage", "invalid ttl: "+err.Error())
			return
		}
		if deps.Engine == nil {
			writeError(w, http.StatusServiceUnavailable, "invariant_violation", "engine not configured")
			return
		}
		if err := deps.Engine.GrantWaiver(r.Context(), "human", runID, req.NodeID, req.GateID, req.Reason, time.Now().Add(ttl)); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invariant_violation", err.Error())
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

type confirmEffectRequest struct {
	NodeID string `json:"nodeId"`
	Effect string `json:"effect"`
	Scope  string `json:"scope"` // "once" | "run"
}

// handleConfirmEffect is the daemon side of `kairos effects confirm` —
// engine.GrantEffectConfirmation's callable core, reachable via CLI/API
// for the first time (L11-policy-secrets.md's Future work).
func handleConfirmEffect(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID := r.PathValue("id")
		var req confirmEffectRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "usage", "invalid request body: "+err.Error())
			return
		}
		if req.NodeID == "" || req.Effect == "" {
			writeError(w, http.StatusBadRequest, "usage", "nodeId and effect are required")
			return
		}
		if req.Scope != "once" && req.Scope != "run" {
			writeError(w, http.StatusBadRequest, "usage", "scope must be \"once\" or \"run\"")
			return
		}
		if deps.Engine == nil {
			writeError(w, http.StatusServiceUnavailable, "invariant_violation", "engine not configured")
			return
		}
		if err := deps.Engine.GrantEffectConfirmation(r.Context(), runID, req.NodeID, req.Effect, req.Scope); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invariant_violation", err.Error())
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}
