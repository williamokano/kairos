package api

import (
	"encoding/json"
	"net/http"

	"github.com/williamokano/kairos/internal/conversation"
	"github.com/williamokano/kairos/internal/domain"
)

func registerConversationRoutes(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("GET /runs/{id}/conversation", handleGetConversation(deps))
	mux.HandleFunc("POST /runs/{id}/conversation/messages", handlePostConversationMessage(deps))
}

type conversationResponse struct {
	Messages []domain.ConversationMessageAppended `json:"messages"`
}

// handleGetConversation serves 09-cli-and-tui.md's Conversation screen —
// read-only here; a future TUI/web surface (L15/L20) is what renders it.
func handleGetConversation(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		msgs, err := conversation.Messages(r.Context(), deps.Store, id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "invariant_violation", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, conversationResponse{Messages: msgs})
	}
}

type postMessageRequest struct {
	Text string `json:"text"`
}

// handlePostConversationMessage is the composer's send action — the only
// way a human currently posts to a Conversation, and (via
// resolveConversationWait's live subscription in internal/engine) the
// only way a wait: conversation node resolves. Always Role: "human" — see
// ConversationMessageAppended's doc comment for why nothing else writes
// here yet.
func handlePostConversationMessage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var req postMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "usage", "invalid request body: "+err.Error())
			return
		}
		if req.Text == "" {
			writeError(w, http.StatusBadRequest, "usage", "text is required")
			return
		}
		if err := conversation.AppendMessage(r.Context(), deps.Store, id, "human", req.Text); err != nil {
			writeError(w, http.StatusInternalServerError, "invariant_violation", err.Error())
			return
		}
		w.WriteHeader(http.StatusCreated)
	}
}
