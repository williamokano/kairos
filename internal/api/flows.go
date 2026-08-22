package api

import (
	"encoding/json"
	"net/http"

	"github.com/williamokano/kairos/internal/registry"
)

type createFlowRequest struct {
	Name string `json:"name"`
	YAML string `json:"yaml"`
}

type flowDefinitionResponse struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// registerFlowRoutes backs `kairos flow create/ls/run` and the web UI's
// flow editor — the durable answer to "there is no way to create a
// workflow definition anywhere in this system" (every prior surface only
// ever REFERENCED a file that had to already exist on disk). Saving goes
// through registry.SaveFlow, which validates via the exact same Load path
// every hand-authored and ad hoc definition already uses — a bad
// workflow is rejected here, at save time, with the real registry error
// text, never silently written and discovered broken by a later `kairos
// run` (AGENTS.md rule 1).
func registerFlowRoutes(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("POST /flow-definitions", func(w http.ResponseWriter, r *http.Request) {
		var req createFlowRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "usage", "invalid request body: "+err.Error())
			return
		}
		if req.Name == "" || req.YAML == "" {
			writeError(w, http.StatusBadRequest, "usage", "name and yaml are required")
			return
		}
		path, err := registry.SaveFlow(deps.Home, req.Name, []byte(req.YAML))
		if err != nil {
			// The real registry.Load/Validate error, verbatim — this is
			// the whole point of validating before writing anywhere
			// durable: the author sees exactly what's wrong, not a
			// generic "save failed."
			writeError(w, http.StatusUnprocessableEntity, "validation_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, flowDefinitionResponse{Name: req.Name, Path: path})
	})

	mux.HandleFunc("GET /flow-definitions", func(w http.ResponseWriter, r *http.Request) {
		flows, err := registry.ListFlowDefinitions(deps.Home)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "invariant_violation", err.Error())
			return
		}
		out := make([]flowDefinitionResponse, 0, len(flows))
		for _, f := range flows {
			out = append(out, flowDefinitionResponse{Name: f.Name, Path: f.Path})
		}
		writeJSON(w, http.StatusOK, map[string]any{"flows": out})
	})

	mux.HandleFunc("GET /flow-definitions/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		info, ok, err := registry.GetFlowDefinition(deps.Home, name)
		if err != nil {
			writeError(w, http.StatusBadRequest, "usage", err.Error())
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "no such flow: "+name)
			return
		}
		writeJSON(w, http.StatusOK, flowDefinitionResponse{Name: info.Name, Path: info.Path})
	})
}
