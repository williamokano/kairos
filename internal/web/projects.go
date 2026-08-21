package web

import (
	"context"
	"net/http"

	"github.com/williamokano/kairos/internal/cli"
)

type projectsPageData struct {
	Projects []cli.Project
	FetchErr error
	Nonce    string
}

// handleProjectsPage is the web counterpart of `kairos project ls` plus
// a composer for `kairos project create` — a real, deliberate extension
// the user asked for directly after live-testing `kairos do`: naming a
// real working directory (git-backed or not) sessions can be bound to.
func handleProjectsPage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		projects, err := deps.Client.ListProjects(ctx)
		if err != nil {
			renderPage(w, "projects", "projects", projectsPageData{FetchErr: err, Nonce: nonce()})
			return
		}
		renderPage(w, "projects", "projects", projectsPageData{Projects: projects, Nonce: nonce()})
	}
}

func handleCreateProject(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			writeComposerError(w, http.StatusBadRequest, "bad form: "+err.Error())
			return
		}
		name := r.PostForm.Get("name")
		path := r.PostForm.Get("path")
		if name == "" || path == "" {
			writeComposerError(w, http.StatusBadRequest, "name and path are required")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		p, err := deps.Client.CreateProject(ctx, name, path)
		if err != nil {
			writeComposerError(w, http.StatusBadGateway, "creating project: "+err.Error())
			return
		}
		renderFragment(w, "frag/projectrow", p)
	}
}
