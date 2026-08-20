package api

import (
	"encoding/json"
	"net/http"

	"github.com/williamokano/kairos/internal/eventstore"
)

type createSourceRequest struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Config   string `json:"config"`
	Flow     string `json:"flow"`
	Project  string `json:"project"`
	Interval int    `json:"intervalSeconds"`
}

type sourceResponse struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	Flow            string `json:"flow,omitempty"`
	Project         string `json:"project,omitempty"`
	IntervalSeconds int    `json:"intervalSeconds"`
	Enabled         bool   `json:"enabled"`
	Health          string `json:"health"`
	HealthReason    string `json:"healthReason,omitempty"`
}

func toSourceResponse(s eventstore.Source) sourceResponse {
	return sourceResponse{
		ID: s.ID, Kind: s.Kind, Flow: s.Flow, Project: s.Project,
		IntervalSeconds: s.IntervalSeconds, Enabled: s.Enabled,
		Health: s.Health.Status, HealthReason: s.Health.Reason,
	}
}

// registerSourceRoutes backs `kairos src add/ls/pause/resume`
// (08-triggers.md) — CRUD over the daemon-owned `source` table, never
// touching a plugin process directly (that happens in
// internal/tasksource.Manager's own goroutines, started at boot).
func registerSourceRoutes(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("POST /sources", func(w http.ResponseWriter, r *http.Request) {
		var req createSourceRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "usage", "invalid request body: "+err.Error())
			return
		}
		if req.ID == "" || req.Kind == "" {
			writeError(w, http.StatusBadRequest, "usage", "id and kind are required")
			return
		}
		if req.Config == "" {
			req.Config = "{}"
		}
		if req.Interval <= 0 {
			req.Interval = 120
		}
		src := eventstore.Source{
			ID: req.ID, Kind: req.Kind, Config: req.Config, Flow: req.Flow, Project: req.Project,
			IntervalSeconds: req.Interval, Enabled: true,
		}
		if err := deps.Store.UpsertSource(r.Context(), src); err != nil {
			writeError(w, http.StatusInternalServerError, "invariant_violation", err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, toSourceResponse(src))
	})

	mux.HandleFunc("GET /sources", func(w http.ResponseWriter, r *http.Request) {
		sources, err := deps.Store.ListSources(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "invariant_violation", err.Error())
			return
		}
		out := make([]sourceResponse, 0, len(sources))
		for _, s := range sources {
			out = append(out, toSourceResponse(s))
		}
		writeJSON(w, http.StatusOK, map[string]any{"sources": out})
	})

	mux.HandleFunc("POST /sources/{id}/pause", setSourceEnabled(deps, false))
	mux.HandleFunc("POST /sources/{id}/resume", setSourceEnabled(deps, true))
}

func setSourceEnabled(deps Deps, enabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, ok, err := deps.Store.GetSource(r.Context(), id); err != nil {
			writeError(w, http.StatusInternalServerError, "invariant_violation", err.Error())
			return
		} else if !ok {
			writeError(w, http.StatusNotFound, "not_found", "no such source: "+id)
			return
		}
		if err := deps.Store.SetSourceEnabled(r.Context(), id, enabled); err != nil {
			writeError(w, http.StatusInternalServerError, "invariant_violation", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}
