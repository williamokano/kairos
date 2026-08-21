package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/williamokano/kairos/internal/conversation"
	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/eventstore"
	"github.com/williamokano/kairos/internal/identity"
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
	// SessionID, when set, elevates continuation to a stable, addressable
	// entity (internal/project.Session) instead of chaining through the
	// last run's id: WorkDir/Actor/the native LLM session id all come
	// from the Session's own record, and every turn — first or Nth —
	// lands in the SAME session.ConversationRunID. Mutually exclusive
	// with ContinueRunID in practice (SessionID wins if both are set);
	// ContinueRunID remains a lower-level escape hatch for a caller that
	// wants to continue one specific run's chat without ever creating a
	// Session at all.
	SessionID string `json:"sessionId,omitempty"`
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
		var kairosSession eventstore.Session
		var haveKairosSession bool

		switch {
		case req.SessionID != "":
			if deps.Projects == nil {
				writeError(w, http.StatusServiceUnavailable, "invariant_violation", "session support not wired into this daemon")
				return
			}
			sess, ok, err := deps.Projects.GetSession(r.Context(), req.SessionID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "invariant_violation", err.Error())
				return
			}
			if !ok {
				writeError(w, http.StatusNotFound, "not_found", "no such session: "+req.SessionID)
				return
			}
			kairosSession, haveKairosSession = sess, true
			opts.Actor = sess.Actor
			opts.WorkDir = sess.WorkDir
			if sess.NativeSessionID != "" {
				opts.ResumeSessionID = sess.NativeSessionID
			}
			if sess.ConversationRunID != "" {
				opts.ConversationRunOverride = sess.ConversationRunID
				conversationRunID = sess.ConversationRunID
			}
		case req.ContinueRunID != "":
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
		if err := conversation.AppendMessageAs(r.Context(), deps.Store, conversationRunID, "human", req.Text, identity.FromRequest(r)); err != nil {
			writeError(w, http.StatusInternalServerError, "invariant_violation", "posting message to conversation: "+err.Error())
			return
		}

		if haveKairosSession {
			// llm.session.started is appended by dispatchLLMActor before
			// the actor's process even finishes (internal/engine/
			// actor_llm.go) — well before the turn's real work
			// completes — so a short bounded wait here (not the full
			// turn) is enough to capture it for the NEXT turn's
			// --resume. If it never appears (engine backlog, a
			// dispatch failure), the next turn simply mints a fresh
			// native session — the same graceful fallback
			// resolveSession already uses for every other
			// session-resume miss in this codebase — rather than this
			// request hanging or failing.
			sid, _, _ := waitForAdHocSessionID(r.Context(), deps.Store, runID, 2*time.Second)
			_ = deps.Projects.RecordTurn(r.Context(), kairosSession.ID, sid, runID)
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

// waitForAdHocSessionID polls lastAdHocSessionID for up to timeout,
// tolerating the ordinary async gap between "run created" (this
// handler's own return) and "the dispatched node's llm.session.started
// fact is durable" — see handleDo's kairosSession branch for why a short
// wait here is worth it (a Kairos Session's very reason to exist is
// resuming the SAME native session on the next turn).
func waitForAdHocSessionID(ctx context.Context, store eventstore.Store, runID string, timeout time.Duration) (string, bool, error) {
	deadline := time.Now().Add(timeout)
	for {
		sid, ok, err := lastAdHocSessionID(ctx, store, runID)
		if err != nil || ok {
			return sid, ok, err
		}
		if time.Now().After(deadline) {
			return "", false, nil
		}
		select {
		case <-ctx.Done():
			return "", false, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
