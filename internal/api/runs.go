package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/eventstore"
	"github.com/williamokano/kairos/internal/tasksource"
)

func registerRunRoutes(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("POST /runs", handleCreateRun(deps))
	mux.HandleFunc("GET /runs", handleListRuns(deps))
	mux.HandleFunc("GET /runs/{id}", handleGetRun(deps))
}

type createRunRequest struct {
	DefinitionPath string          `json:"definitionPath"`
	Params         json.RawMessage `json:"params,omitempty"`
	// IdempotencyKey, if set, dedupes a retried/double-submitted POST —
	// NL-49 (11-limitations.md): the web composer already minted this
	// value (rendered as a hidden form "nonce") but the daemon never read
	// it. A second request with the same key returns the run the first
	// one created, rather than creating a new one.
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
}

type createRunResponse struct {
	RunID  string `json:"runId"`
	Status string `json:"status"`
}

// handleCreateRun calls tasksource.CreateRun — the one code path
// L16-triggers.md establishes for every way a Run comes into existence,
// `kairos run` included (TriggerRef "cli:kairos-run"). Originally
// (L04-daemon-api-cli.md decision #1) this handler contained the
// TriggerReceived/RunStarted append sequence directly; it now delegates
// so internal/api and every trigger source in internal/tasksource share
// exactly one implementation, not two that could drift.
//
// A CLI-initiated run passes QueueLimits{} (unchecked) — 08-triggers.md's
// maxQueued/maxOpenDecisions backpressure targets trigger-created
// backlog, not a human typing a command right now.
func handleCreateRun(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createRunRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "usage", "invalid request body: "+err.Error())
			return
		}
		if req.DefinitionPath == "" {
			writeError(w, http.StatusBadRequest, "usage", "definitionPath is required")
			return
		}

		if req.IdempotencyKey != "" {
			existingRunID, isNew, err := deps.Store.DedupeRunCreation(r.Context(), req.IdempotencyKey)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "invariant_violation", err.Error())
				return
			}
			if !isNew {
				if existingRunID == "" {
					// A concurrent creator claimed this key an instant ago
					// and hasn't called RecordRunCreation yet — the same
					// rare race DedupeTrigger's own doc comment names.
					// There is no run to return; asking the caller to
					// retry is honest, not a silent duplicate.
					writeError(w, http.StatusConflict, "idempotency_key_pending", "a run for this idempotency key is still being created")
					return
				}
				state, ok, err := deps.Store.GetRunState(r.Context(), existingRunID)
				if err != nil {
					writeError(w, http.StatusInternalServerError, "invariant_violation", err.Error())
					return
				}
				if !ok {
					writeError(w, http.StatusInternalServerError, "invariant_violation", "idempotency key resolved to a run that no longer exists: "+existingRunID)
					return
				}
				writeJSON(w, http.StatusOK, createRunResponse{RunID: existingRunID, Status: string(state.Status)})
				return
			}
		}

		runID, status, err := tasksource.CreateRun(r.Context(), deps.Store, tasksource.CreateRunRequest{
			DefinitionRef: req.DefinitionPath,
			Params:        req.Params,
			TriggerRef:    "cli:kairos-run",
			Actor:         "cli",
		}, tasksource.QueueLimits{})
		if err != nil {
			if errors.Is(err, tasksource.ErrQueueFull) || errors.Is(err, tasksource.ErrTooManyOpenDecisions) {
				writeError(w, http.StatusTooManyRequests, "rejected", err.Error())
				return
			}
			var verr *tasksource.ValidationError
			if errors.As(err, &verr) {
				writeError(w, http.StatusUnprocessableEntity, "validation_failed", err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, "invariant_violation", err.Error())
			return
		}

		if req.IdempotencyKey != "" {
			if err := deps.Store.RecordRunCreation(r.Context(), req.IdempotencyKey, runID); err != nil {
				writeError(w, http.StatusInternalServerError, "invariant_violation", err.Error())
				return
			}
		}

		writeJSON(w, http.StatusCreated, createRunResponse{RunID: runID, Status: string(status)})
	}
}

type listRunsResponse struct {
	Runs []eventstore.RunSummary `json:"runs"`
}

func handleListRuns(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var status *domain.RunStatus
		if s := r.URL.Query().Get("status"); s != "" {
			rs := domain.RunStatus(s)
			status = &rs
		}
		runs, err := deps.Store.ListRuns(r.Context(), status)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "invariant_violation", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, listRunsResponse{Runs: runs})
	}
}

func handleGetRun(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		state, ok, err := deps.Store.GetRunState(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "invariant_violation", err.Error())
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "no such run: "+id)
			return
		}
		writeJSON(w, http.StatusOK, state)
	}
}
