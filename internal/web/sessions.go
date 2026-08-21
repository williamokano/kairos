package web

import (
	"context"
	"net/http"

	"github.com/williamokano/kairos/internal/cli"
)

type sessionsPageData struct {
	Sessions []cli.Session
	Projects []cli.Project
	FetchErr error
	Nonce    string
}

// handleSessionsPage is the web counterpart of `kairos session ls` plus
// a composer for `kairos session start` — a stable, resumable chat
// identity a user creates once and sends many `kairos do --session <id>`
// turns to (linked from the chat page's session picker).
func handleSessionsPage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		sessions, err := deps.Client.ListSessions(ctx)
		if err != nil {
			renderPage(w, "sessions", "sessions", sessionsPageData{FetchErr: err, Nonce: nonce()})
			return
		}
		projects, _ := deps.Client.ListProjects(ctx) // best-effort — an empty picker still lets a project-less session start
		renderPage(w, "sessions", "sessions", sessionsPageData{Sessions: sessions, Projects: projects, Nonce: nonce()})
	}
}

func handleCreateSession(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			writeComposerError(w, http.StatusBadRequest, "bad form: "+err.Error())
			return
		}
		projectName := r.PostForm.Get("project")
		actor := r.PostForm.Get("actor")
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		s, err := deps.Client.StartSession(ctx, projectName, actor)
		if err != nil {
			writeComposerError(w, http.StatusBadGateway, "starting session: "+err.Error())
			return
		}
		renderFragment(w, "frag/sessionrow", s)
	}
}
