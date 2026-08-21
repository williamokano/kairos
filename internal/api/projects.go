package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/williamokano/kairos/internal/eventstore"
	"github.com/williamokano/kairos/internal/identity"
)

type createProjectRequest struct {
	Name     string `json:"name"`
	RepoPath string `json:"repoPath"`
}

type projectResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	RepoPath  string `json:"repoPath"`
	GitBacked bool   `json:"gitBacked"`
	CreatedBy string `json:"createdBy,omitempty"`
	CreatedAt string `json:"createdAt"`
}

func toProjectResponse(p eventstore.Project) projectResponse {
	return projectResponse{
		ID: p.ID, Name: p.Name, RepoPath: p.RepoPath, GitBacked: p.GitBacked,
		CreatedBy: p.CreatedBy, CreatedAt: p.CreatedAt.Format(time.RFC3339),
	}
}

// registerProjectRoutes backs `kairos project create/ls` — a real,
// deliberate extension the user asked for after live-testing `kairos
// do`: a named binding to a real working directory, git-backed or not
// (auto-detected — internal/project.CreateProject).
func registerProjectRoutes(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("POST /projects", func(w http.ResponseWriter, r *http.Request) {
		if deps.Projects == nil {
			writeError(w, http.StatusServiceUnavailable, "invariant_violation", "project support not wired into this daemon")
			return
		}
		var req createProjectRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "usage", "invalid request body: "+err.Error())
			return
		}
		if req.Name == "" || req.RepoPath == "" {
			writeError(w, http.StatusBadRequest, "usage", "name and repoPath are required")
			return
		}
		p, err := deps.Projects.CreateProject(r.Context(), req.Name, req.RepoPath, identity.FromRequest(r))
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, toProjectResponse(p))
	})

	mux.HandleFunc("GET /projects", func(w http.ResponseWriter, r *http.Request) {
		if deps.Projects == nil {
			writeJSON(w, http.StatusOK, map[string]any{"projects": []projectResponse{}})
			return
		}
		projects, err := deps.Projects.ListProjects(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "invariant_violation", err.Error())
			return
		}
		out := make([]projectResponse, 0, len(projects))
		for _, p := range projects {
			out = append(out, toProjectResponse(p))
		}
		writeJSON(w, http.StatusOK, map[string]any{"projects": out})
	})
}
