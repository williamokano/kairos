package web

import (
	"context"
	"net/http"

	"github.com/williamokano/kairos/internal/cli"
)

// registerSessionChatRoutes wires the session-centric chat page the user
// asked for directly after finding the plain /chat page's two-step
// "pick a session, then type" flow silently droppable: this page is
// keyed by the session's own ID in the URL PATH, not a form field a
// submit can omit, so there is no way to send a message that forgets
// which session (and therefore which real WorkDir/native LLM session) it
// belongs to.
func registerSessionChatRoutes(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("GET /sessions/{id}", handleSessionChatPage(deps))
	mux.HandleFunc("POST /sessions/{id}/messages", handleSessionChatSend(deps))
	mux.HandleFunc("GET /frag/session/{id}/messages", handleSessionChatMessagesFrag(deps))
}

type sessionChatPageData struct {
	Session   cli.Session
	Messages  []cli.ConversationMessage
	FormNonce string
	FetchErr  error
}

// handleSessionChatPage shows a session's ENTIRE chat history — every
// turn it has ever had, since every turn's message (and reply) lands in
// the same session.ConversationRunID (see L24-kairos-do.md/
// internal/api/do.go) — rendered as one continuous thread, exactly the
// "table of inputs and outputs, just like a conversation" the user asked
// for, with one persistent input box.
func handleSessionChatPage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		sess, err := deps.Client.GetSession(ctx, id)
		if err != nil {
			renderPage(w, "session", "sessionchat", sessionChatPageData{FetchErr: err, FormNonce: nonce()}, id)
			return
		}
		data := sessionChatPageData{Session: sess, FormNonce: nonce()}
		if sess.ConversationRunID != "" {
			msgs, err := deps.Client.GetConversation(ctx, sess.ConversationRunID)
			if err == nil {
				data.Messages = msgs
			}
		}
		renderPage(w, id, "sessionchat", data, id)
	}
}

// handleSessionChatSend always threads THIS session's id from the URL
// path — never a form field, so there is no way to silently lose it the
// way the plain /chat page's two-step picker allowed (the exact bug that
// produced the user's "empty folder" reply: the session was picked, but
// the message that actually ran carried no sessionId at all).
func handleSessionChatSend(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := r.ParseForm(); err != nil {
			writeChatError(w, http.StatusBadRequest, "bad form: "+err.Error())
			return
		}
		text := r.PostForm.Get("text")
		if text == "" {
			writeChatError(w, http.StatusBadRequest, "text is required")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		if _, err := deps.Client.DoWithSession(ctx, text, "", id); err != nil {
			writeChatError(w, http.StatusBadGateway, "sending: "+err.Error())
			return
		}
		dest := "/sessions/" + id
		w.Header().Set("HX-Redirect", dest)
		http.Redirect(w, r, dest, http.StatusSeeOther)
	}
}

// handleSessionChatMessagesFrag backs the session page's live update —
// real SSE on the conversation's own stream, matching handleChatMessagesFrag's
// established pattern exactly, just re-resolved through the session's
// ConversationRunID (a session's SSE-visible stream id is only known
// after loading the session, unlike /chat's plain per-run page where the
// run id IS the stream key).
func handleSessionChatMessagesFrag(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		sess, err := deps.Client.GetSession(ctx, id)
		if err != nil || sess.ConversationRunID == "" {
			renderFragment(w, "frag/chatmessages", []cli.ConversationMessage{})
			return
		}
		msgs, err := deps.Client.GetConversation(ctx, sess.ConversationRunID)
		if err != nil {
			http.Error(w, "loading conversation: "+err.Error(), http.StatusBadGateway)
			return
		}
		renderFragment(w, "frag/chatmessages", msgs)
	}
}
