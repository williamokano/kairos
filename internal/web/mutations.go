package web

import (
	"context"
	"html"
	"net/http"
	"strconv"

	"github.com/williamokano/kairos/internal/cli"
)

// registerMutations wires 10-webui.md's mutating routes' daemon-side
// operations. L20-webui.md's original pass built start/answer/message
// only, naming cancel/fork/say/source-pause as deferred. This pass
// (L23-webui-revamp.md) closes cancel/fork/source-pause: each dialog
// requires a server-enforced typed confirmation — POSTing straight to
// these routes without the exact matching confirm field is rejected with
// 422, the identical "client-side gating is a UX aid, the server decides"
// discipline the decision page's typed-word check already established
// (see handleAnswerDecision's doc comment). "say" (injecting a message
// into a LIVE session mid-execution, distinct from conversation send,
// which already exists and is wired) has no daemon capability anywhere in
// this tree — see L23-webui-revamp.md's Documented decisions for why it
// stays unbuilt rather than inventing new engine/actor infrastructure for
// it under a "web UI dialogs" document.
func registerMutations(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("POST /runs", handleCreateRun(deps))
	mux.HandleFunc("POST /t/{runID}/{nodeID}/answer", handleAnswerDecision(deps))
	mux.HandleFunc("POST /c/{runID}/messages", handlePostMessage(deps))
	mux.HandleFunc("POST /runs/{id}/cancel", handleCancelRun(deps))
	mux.HandleFunc("POST /runs/{id}/fork", handleForkRun(deps))
	mux.HandleFunc("POST /sources/{id}/pause", handlePauseSource(deps))
	mux.HandleFunc("POST /projects", handleCreateProject(deps))
	mux.HandleFunc("POST /sessions", handleCreateSession(deps))
}

func handleCreateRun(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			writeComposerError(w, http.StatusBadRequest, "bad form: "+err.Error())
			return
		}
		defPath := r.PostForm.Get("definitionPath")
		if defPath == "" {
			writeComposerError(w, http.StatusBadRequest, "definitionPath is required")
			return
		}
		// NL-49's fix: the composer's hidden "nonce" field (pages.go's
		// nonce()) was rendered and posted but never actually forwarded —
		// a double-click or a retried request after a dropped response
		// created two runs instead of one. Now genuinely deduped
		// server-side (internal/api's DedupeRunCreation).
		idempotencyKey := r.PostForm.Get("nonce")
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		resp, err := deps.Client.CreateRun(ctx, defPath, nil, idempotencyKey)
		if err != nil {
			// A plain http.Error here used to be swallowed silently: htmx
			// only swaps a response's body into the DOM on 2xx, so a
			// non-2xx error rendered nothing at all — "click run and
			// nothing happens," AGENTS.md rule 1's exact silent-failure
			// trap. hx-target-error routes this same response into
			// #composer-error instead via htmx's response-targeting
			// extension, so a real failure is always visible.
			writeComposerError(w, http.StatusBadGateway, "creating run: "+err.Error())
			return
		}
		renderFragment(w, "frag/runrow", struct {
			RunID, Status string
		}{resp.RunID, resp.Status})
	}
}

// writeComposerError renders an HTML fragment (not plain text) so it can
// be swapped into the page even on a non-2xx response via htmx's
// response-targeting extension (hx-target-error on the composer's form).
func writeComposerError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`<p class="error">` + html.EscapeString(msg) + `</p>`))
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

// requireTypedConfirm is the server-side half of every destructive web
// dialog's anti-rubber-stamp discipline: the form's hidden/typed
// "confirm" field must equal want exactly, or the request is rejected —
// mirroring handleAnswerDecision's typed-word check (both ultimately
// enforce "the client cannot get through by skipping its own dialog").
// Unlike the decision page's typedWord (checked engine-side, against
// engine state), this check is purely a web-layer invariant: cancel/fork/
// source-pause have no engine-level typed-confirm concept of their own
// (neither the CLI nor the TUI's own y/n prompt for x/f/Q requires one —
// see 09-cli-and-tui.md's keys.go comment), so this is real, new,
// web-mutation-layer enforcement, not a relaxed copy of an existing check.
func requireTypedConfirm(w http.ResponseWriter, r *http.Request, want string) bool {
	if r.PostForm.Get("confirm") == want {
		return true
	}
	w.WriteHeader(http.StatusUnprocessableEntity)
	_, _ = w.Write([]byte(`<p class="error">confirmation text did not match — nothing was done</p>`))
	return false
}

// handleCancelRun is the web dialog for `kairos cancel` (internal/api's
// new POST /runs/{id}/cancel — see internal/engine/cancel.go). Compensation
// of applied effects, if any, is automatic and unconditional (shard.go),
// not a checkbox this form offers, because there is nothing to toggle.
func handleCancelRun(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID := r.PathValue("id")
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
			return
		}
		if !requireTypedConfirm(w, r, runID) {
			return
		}
		reason := r.PostForm.Get("reason")
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		if err := deps.Client.Cancel(ctx, runID, reason); err != nil {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`<p class="error">` + html.EscapeString(err.Error()) + `</p>`))
			return
		}
		_, _ = w.Write([]byte(`<div class="mutation-done">cancelled — <a href="/runs/` + runID + `">back to run</a></div>`))
	}
}

// handleForkRun is the web dialog for `kairos fork` (internal/cli.Client.Fork
// already existed — L18 — this pass adds only the web presentation, per
// L20-webui.md's original Future work entry). --allow-drift is an explicit
// checkbox, never a default-on behavior, matching newForkCmd's own doc
// comment ("no implicit-yes default").
func handleForkRun(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID := r.PathValue("id")
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
			return
		}
		if !requireTypedConfirm(w, r, runID) {
			return
		}
		atSequence, _ := strconv.Atoi(r.PostForm.Get("atSequence"))
		allowDrift := r.PostForm.Get("allowDrift") == "on"
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		result, err := deps.Client.Fork(ctx, runID, atSequence, nil, allowDrift)
		if err != nil {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`<p class="error">` + html.EscapeString(err.Error()) + `</p>`))
			return
		}
		_, _ = w.Write([]byte(`<div class="mutation-done">forked — <a href="/runs/` + result.NewRunID + `">` + result.NewRunID + `</a></div>`))
	}
}

// handlePauseSource is the web dialog for `kairos src pause`
// (internal/cli.Client.PauseSource already existed — L16 — this pass adds
// only the web presentation).
func handlePauseSource(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
			return
		}
		if !requireTypedConfirm(w, r, id) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		if err := deps.Client.PauseSource(ctx, id); err != nil {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`<p class="error">` + html.EscapeString(err.Error()) + `</p>`))
			return
		}
		_, _ = w.Write([]byte(`<div class="mutation-done">paused — <a href="/sources">back to sources</a></div>`))
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
