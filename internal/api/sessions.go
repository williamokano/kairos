package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/williamokano/kairos/internal/eventstore"
	"github.com/williamokano/kairos/internal/identity"
)

type createSessionRequest struct {
	ProjectID   string `json:"projectId"`
	ProjectName string `json:"projectName"`
	Actor       string `json:"actor"`
}

type sessionResponse struct {
	ID              string `json:"id"`
	ProjectID       string `json:"projectId,omitempty"`
	Actor           string `json:"actor"`
	WorkDir         string `json:"workDir"`
	Branch          string `json:"branch,omitempty"`
	NativeSessionID string `json:"nativeSessionId,omitempty"`
	LastRunID       string `json:"lastRunId,omitempty"`
	// ConversationRunID is where this session's ONE continuous chat
	// thread actually lives (set on its first turn, never overwritten —
	// see eventstore.Session's own doc comment). Exposed so a web/CLI
	// client can go straight to a session's real conversation without
	// re-deriving it — the previous omission of this field is exactly
	// why the web UI's session picker and the plain Conversation page
	// were two disconnected flows (see L26-session-chat.md's Documented
	// decisions).
	ConversationRunID string `json:"conversationRunId,omitempty"`
	RunCount          int    `json:"runCount"`
	CreatedBy         string `json:"createdBy,omitempty"`
	LastUsedAt        string `json:"lastUsedAt"`
}

func toSessionResponse(s eventstore.Session) sessionResponse {
	return sessionResponse{
		ID: s.ID, ProjectID: s.ProjectID, Actor: s.Actor, WorkDir: s.WorkDir, Branch: s.Branch,
		NativeSessionID: s.NativeSessionID, LastRunID: s.LastRunID, ConversationRunID: s.ConversationRunID,
		RunCount: s.RunCount, CreatedBy: s.CreatedBy, LastUsedAt: s.LastUsedAt.Format(time.RFC3339),
	}
}

// registerSessionRoutes backs `kairos session start/ls` — a stable,
// resumable chat identity (internal/project.Session), elevating
// `kairos do --continue`'s run-chained continuation into a first-class,
// addressable entity a user can create once and send many turns to.
func registerSessionRoutes(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("POST /sessions", func(w http.ResponseWriter, r *http.Request) {
		if deps.Projects == nil {
			writeError(w, http.StatusServiceUnavailable, "invariant_violation", "session support not wired into this daemon")
			return
		}
		var req createSessionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "usage", "invalid request body: "+err.Error())
			return
		}
		projectID := req.ProjectID
		if projectID == "" && req.ProjectName != "" {
			p, ok, err := deps.Projects.GetProjectByName(r.Context(), req.ProjectName)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "invariant_violation", err.Error())
				return
			}
			if !ok {
				writeError(w, http.StatusNotFound, "not_found", "no such project: "+req.ProjectName)
				return
			}
			projectID = p.ID
		}
		actor := req.Actor
		if actor == "" {
			actor = deps.DefaultDoActor
		}
		if actor == "" {
			writeError(w, http.StatusBadRequest, "usage", "actor is required (no DefaultDoActor configured)")
			return
		}

		// A project-less session still needs a real scratch dir (the
		// same convention POST /do already uses for a plain ad hoc run) —
		// internal/project.StartSession's own doc comment covers why this
		// isn't just "" (an llm actor's WorkDirOverride must be a real,
		// existing directory it can chdir into).
		scratchDir := filepath.Join(deps.Home, "sessions", "ses_"+ulid.Make().String())
		if err := os.MkdirAll(scratchDir, 0o700); err != nil {
			writeError(w, http.StatusInternalServerError, "invariant_violation", fmt.Sprintf("creating session scratch dir: %s", err))
			return
		}

		sess, err := deps.Projects.StartSession(r.Context(), projectID, actor, scratchDir, identity.FromRequest(r))
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, toSessionResponse(sess))
	})

	mux.HandleFunc("GET /sessions", func(w http.ResponseWriter, r *http.Request) {
		if deps.Projects == nil {
			writeJSON(w, http.StatusOK, map[string]any{"sessions": []sessionResponse{}})
			return
		}
		sessions, err := deps.Projects.ListSessions(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "invariant_violation", err.Error())
			return
		}
		out := make([]sessionResponse, 0, len(sessions))
		for _, s := range sessions {
			out = append(out, toSessionResponse(s))
		}
		writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
	})

	// GET /sessions/{id} backs `kairos session show <id>` and the web
	// UI's session-centric chat page (/sessions/{id}) — a single
	// session's own record, including ConversationRunID, without a
	// caller having to scan the full list to find one.
	mux.HandleFunc("GET /sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		if deps.Projects == nil {
			writeError(w, http.StatusServiceUnavailable, "invariant_violation", "session support not wired into this daemon")
			return
		}
		sess, ok, err := deps.Projects.GetSession(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "invariant_violation", err.Error())
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "no such session: "+r.PathValue("id"))
			return
		}
		writeJSON(w, http.StatusOK, toSessionResponse(sess))
	})
}
