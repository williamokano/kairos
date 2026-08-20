package web

import (
	"context"
	"html"
	"net/http"

	"github.com/williamokano/kairos/internal/cli"
)

// registerMutations wires 10-webui.md's nine mutating routes' daemon-side
// operations that this pass implements: start a run, answer a decision,
// post a conversation message. cancel/fork/say/source-pause are named in
// the route map but deferred — see L20-webui.md's Future work; the
// underlying cli.Client methods already exist (L18/L14/L16), so wiring
// them is presentation work, not new daemon capability.
func registerMutations(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("POST /runs", handleCreateRun(deps))
	mux.HandleFunc("POST /t/{runID}/{nodeID}/answer", handleAnswerDecision(deps))
	mux.HandleFunc("POST /c/{runID}/messages", handlePostMessage(deps))
}

func handleCreateRun(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
			return
		}
		defPath := r.PostForm.Get("definitionPath")
		if defPath == "" {
			http.Error(w, "definitionPath is required", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		resp, err := deps.Client.CreateRun(ctx, defPath, nil)
		if err != nil {
			http.Error(w, "creating run: "+err.Error(), http.StatusBadGateway)
			return
		}
		renderFragment(w, "frag/runrow", struct {
			RunID, Status string
		}{resp.RunID, resp.Status})
	}
}

// handleAnswerDecision posts the form straight through to the existing
// `POST /runs/{id}/approve` route — the SAME server-side validation
// (reason required, typed word required and checked against the node id)
// that already backs `kairos approve` and the TUI's decision screen. No
// parallel approval path, no relaxed rule for the browser.
func handleAnswerDecision(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID, nodeID := r.PathValue("runID"), r.PathValue("nodeID")
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
			return
		}
		decision := r.PostForm.Get("decision")
		reason := r.PostForm.Get("reason")
		typedWord := r.PostForm.Get("typedWord")

		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		err := deps.Client.ApproveHumanTask(ctx, runID, nodeID, decision, reason, typedWord)
		if err != nil {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`<p class="error">` + html.EscapeString(err.Error()) + `</p>`))
			return
		}
		_, _ = w.Write([]byte(`<div class="decision-answered">answered — <a href="/runs/` + runID + `">back to run</a></div>`))
	}
}

func handlePostMessage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID := r.PathValue("runID")
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
			return
		}
		text := r.PostForm.Get("text")
		if text == "" {
			http.Error(w, "text is required", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		if err := deps.Client.PostConversationMessage(ctx, runID, text); err != nil {
			http.Error(w, "posting message: "+err.Error(), http.StatusBadGateway)
			return
		}
		renderFragment(w, "frag/message", cli.ConversationMessage{Role: "you", Text: text})
	}
}
