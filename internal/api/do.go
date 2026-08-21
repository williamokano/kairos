package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/oklog/ulid/v2"

	"github.com/williamokano/kairos/internal/conversation"
	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/eventstore"
	"github.com/williamokano/kairos/internal/registry"
	"github.com/williamokano/kairos/internal/tasksource"
)

func registerDoRoutes(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("POST /do", handleDo(deps))
}

type doRequest struct {
	Text string `json:"text"`
	// ContinueRunID, when set, is an existing ad hoc run's Conversation to
	// continue rather than start fresh — see doRequest's handler doc
	// comment for the full turn-2+ mechanics.
	ContinueRunID string `json:"continueRunId,omitempty"`
}

type doResponse struct {
	RunID string `json:"runId"`
	// ConversationRunID is where the thread actually lives — equal to
	// RunID on turn one; on a continuation it's ContinueRunID, since a
	// NEW run does the LLM work each turn (see SynthesizeAdHoc's doc
	// comment) but every turn's messages land in the SAME conversation.
	ConversationRunID string `json:"conversationRunId"`
}

// handleDo is `kairos do` / the web chat / the TUI composer's single
// entrypoint — 09-cli-and-tui.md's and L15-tui.md's named-but-unbuilt gap
// ("kairos do... needs a daemon-side endpoint accepting free text"),
// closed here. It goes through tasksource.CreateRun, the SAME one code
// path every other trigger source uses (L16-triggers.md) — a real,
// user-initiated request is exactly what 01-architecture.md's L15 ("Kairos
// never invents work") requires a Run to trace back to; this handler
// synthesizes the workflow, not the decision to run it.
//
// Continuation (turn 2+): a chat's session cannot resume within the SAME
// run (resolveSession's prior-attempt lookup in internal/engine/actor_llm.go
// is scoped to one run's own event stream, and a node with wait:conversation
// can never also carry an actor — domain.dispatchExec routes Wait-bearing
// nodes straight to Waiting, never spawning one — see
// L24-kairos-do.md's Documented decisions for why a genuine single-run
// chat loop needs new engine primitives this pass doesn't build). So each
// turn is its OWN new run, whose ad hoc node carries the prior run's real
// session ID (read off its last llm.session.started event) as
// ResumeSessionID, giving a REAL native --resume, and whose
// ConversationRunOverride targets ContinueRunID so every turn's reply
// still lands in one continuous thread.
func handleDo(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req doRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "usage", "invalid request body: "+err.Error())
			return
		}
		if req.Text == "" {
			writeError(w, http.StatusBadRequest, "usage", "text is required")
			return
		}

		opts := registry.AdHocOptions{Actor: deps.DefaultDoActor}
		conversationRunID := req.ContinueRunID
		if req.ContinueRunID != "" {
			sessionID, ok, err := lastAdHocSessionID(r.Context(), deps.Store, req.ContinueRunID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "invariant_violation", err.Error())
				return
			}
			if !ok {
				writeError(w, http.StatusUnprocessableEntity, "validation_failed", "continueRunId has no prior ad hoc session to resume: "+req.ContinueRunID)
				return
			}
			opts.ResumeSessionID = sessionID
			opts.ConversationRunOverride = req.ContinueRunID
		}

		defPath, err := registry.SynthesizeAdHoc(deps.Home, req.Text, opts)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "invariant_violation", "synthesizing ad hoc definition: "+err.Error())
			return
		}

		runID, _, err := tasksource.CreateRun(r.Context(), deps.Store, tasksource.CreateRunRequest{
			DefinitionRef: defPath,
			TriggerRef:    "do:" + ulid.Make().String(),
			Actor:         "trigger:do",
		}, tasksource.QueueLimits{}) // unchecked — a live "do" request is a human acting now, matching kairos run's own exemption (L16-triggers.md's documented precedent)
		if err != nil {
			var verr *tasksource.ValidationError
			if errors.As(err, &verr) {
				writeError(w, http.StatusUnprocessableEntity, "validation_failed", err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, "invariant_violation", err.Error())
			return
		}

		if conversationRunID == "" {
			conversationRunID = runID
		}
		if err := conversation.AppendMessage(r.Context(), deps.Store, conversationRunID, "human", req.Text); err != nil {
			writeError(w, http.StatusInternalServerError, "invariant_violation", "posting message to conversation: "+err.Error())
			return
		}

		writeJSON(w, http.StatusCreated, doResponse{RunID: runID, ConversationRunID: conversationRunID})
	}
}

// lastAdHocSessionID reads runID's own event stream for the most recent
// llm.session.started against node "task" (SynthesizeAdHoc's node id is
// always "task") — the same read shape as
// internal/engine.(*Engine).priorSession, duplicated rather than
// imported: internal/api may not import internal/engine's dispatch
// internals (Deps.Engine exists only for AnswerHumanTask's narrow
// exception, documented at its own call site), and this read is a
// three-line scan, not worth a new shared package for.
func lastAdHocSessionID(ctx context.Context, store eventstore.Store, runID string) (string, bool, error) {
	envs, err := store.Read(ctx, runID)
	if err != nil {
		return "", false, err
	}
	var sessionID string
	found := false
	for _, env := range envs {
		s, ok := env.Event.(domain.LLMSessionStarted)
		if !ok || s.NodeID != "task" {
			continue
		}
		sessionID = s.SessionID
		found = true
	}
	return sessionID, found, nil
}
