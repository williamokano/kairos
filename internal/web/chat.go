package web

import (
	"context"
	"net/http"

	"github.com/williamokano/kairos/internal/cli"
)

func registerChatRoutes(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("GET /chat", handleChatPage(deps))
	mux.HandleFunc("POST /chat", handleChatSend(deps))
	mux.HandleFunc("GET /frag/chat/{runID}/messages", handleChatMessagesFrag(deps))
}

type chatPageData struct {
	RunID     string // the conversation being continued, "" for a fresh chat
	Messages  []cli.ConversationMessage
	FormNonce string
}

// handleChatPage is the web UI's real-time-chat surface — 10-webui.md's
// home-page composer reimagined for `kairos do` (L20-webui.md's home
// composer stays file-path-only; this is a NEW, dedicated page rather
// than reworking that one, so a workflow author's normal "run this file"
// flow is untouched — see L24-kairos-do.md's Documented decisions). With
// no ?run= it shows a bare composer; with one, the growing thread plus a
// composer whose hidden continueRunId keeps every later turn in the SAME
// conversation.
func handleChatPage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID := r.URL.Query().Get("run")
		data := chatPageData{RunID: runID, FormNonce: nonce()}
		if runID != "" {
			ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
			defer cancel()
			msgs, err := deps.Client.GetConversation(ctx, runID)
			if err != nil {
				http.Error(w, "loading conversation: "+err.Error(), http.StatusBadGateway)
				return
			}
			data.Messages = msgs
		}
		renderPage(w, "chat", "chat", data, runID)
	}
}

// handleChatSend posts to the SAME daemon endpoint `kairos do` and the
// TUI composer use (cli.Client.Do -> POST /do) — the web UI is a THIRD
// client of that one entrypoint, never a private path (ADR 0008's "the
// terminal is a client" extended to the browser, same posture every other
// page in this package already holds). On success it redirects to
// /chat?run=<conversationRunId> — a real page reload rather than an htmx
// swap, since the very first turn's conversation didn't exist a moment
// ago and a full navigation is the simplest correct way to start
// following it (matching how the fork/cancel dialogs elsewhere in this
// package already redirect rather than swap after a state-creating
// mutation).
func handleChatSend(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			writeChatError(w, http.StatusBadRequest, "bad form: "+err.Error())
			return
		}
		text := r.PostForm.Get("text")
		if text == "" {
			writeChatError(w, http.StatusBadRequest, "text is required")
			return
		}
		continueRunID := r.PostForm.Get("continueRunId")
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		resp, err := deps.Client.Do(ctx, text, continueRunID)
		if err != nil {
			writeChatError(w, http.StatusBadGateway, "sending: "+err.Error())
			return
		}
		dest := "/chat?run=" + resp.ConversationRunID
		// htmx's own XHR would otherwise follow a plain 3xx and swap the
		// destination page's body into #chat-error (this form's small
		// hx-target) — HX-Redirect tells htmx to do a real, full-page
		// client-side navigation instead, exactly like a non-JS browser
		// following the same Location header would.
		w.Header().Set("HX-Redirect", dest)
		http.Redirect(w, r, dest, http.StatusSeeOther)
	}
}

// handleChatMessagesFrag backs the chat page's live update — real SSE on
// the conversation's own stream (see chat.gohtml's sse-connect, reusing
// /frag/run/{id}/events verbatim: it proxies whatever stream id it's
// given, and eventstore.ConversationStreamID's "conversation:"+runID
// value is just another valid stream id) triggers a refetch of this
// fragment rather than a 2-second poll — L15-tui.md's own Future work
// item #1 named "replace the poll with real SSE push" as the honest
// standard for this codebase, and the SSE proxy already exists, so this
// page gets that for free rather than regressing to polling.
func handleChatMessagesFrag(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID := r.PathValue("runID")
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		msgs, err := deps.Client.GetConversation(ctx, runID)
		if err != nil {
			http.Error(w, "loading conversation: "+err.Error(), http.StatusBadGateway)
			return
		}
		renderFragment(w, "frag/chatmessages", msgs)
	}
}

func writeChatError(w http.ResponseWriter, status int, msg string) {
	writeComposerError(w, status, msg)
}
