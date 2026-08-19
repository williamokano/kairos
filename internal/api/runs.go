package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/eventstore"
	"github.com/williamokano/kairos/internal/registry"
)

func registerRunRoutes(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("POST /runs", handleCreateRun(deps))
	mux.HandleFunc("GET /runs", handleListRuns(deps))
	mux.HandleFunc("GET /runs/{id}", handleGetRun(deps))
}

type createRunRequest struct {
	DefinitionPath string          `json:"definitionPath"`
	Params         json.RawMessage `json:"params,omitempty"`
}

type createRunResponse struct {
	RunID  string `json:"runId"`
	Status string `json:"status"`
}

// handleCreateRun is decision #1 in L04-daemon-api-cli.md: it appends
// TriggerReceived, then folds domain.Advance in-process ONCE to also
// append RunStarted{Graph}. The resulting []Cmd is discarded — there is
// no dispatch loop here, no Cmd execution, no process spawn. This is not
// the engine (L05); it is the same single fold RunStateProjection already
// performs inside AppendIf's own transaction, done once more synchronously
// so DefinitionRef resolves to a Graph before anything needs it.
//
// The two AppendIf calls are NOT one transaction: a daemon crash between
// them leaves a run at TriggerReceived only. This is a registered,
// documented interim gap (11-limitations.md) that L05's reconciliation
// must recognise, not solved here.
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
		absPath, err := filepath.Abs(req.DefinitionPath)
		if err != nil {
			writeError(w, http.StatusBadRequest, "usage", "resolving definitionPath: "+err.Error())
			return
		}

		def, err := registry.Load(absPath)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed", err.Error())
			return
		}
		graph, err := registry.ProjectGraph(def)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed", "projecting graph: "+err.Error())
			return
		}

		runID := ulid.Make().String()
		now := time.Now().UTC()
		meta := eventstore.AppendMeta{Actor: "cli", CorrelationID: runID, OccurredAt: now}

		trigger := domain.TriggerReceived{
			RunID:         runID,
			TriggerRef:    "cli:kairos-run",
			DefinitionRef: absPath,
			Params:        req.Params,
			CorrelationID: runID,
		}
		if _, err := deps.Store.AppendIf(r.Context(), runID, 0, []domain.Event{trigger}, meta); err != nil {
			writeError(w, http.StatusInternalServerError, "invariant_violation", "appending trigger.received: "+err.Error())
			return
		}

		state, _, err := domain.Advance(domain.RunState{}, trigger, now)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "invariant_violation", "folding trigger.received: "+err.Error())
			return
		}
		started := domain.RunStarted{RunID: runID, Graph: graph}
		state, cmds, err := domain.Advance(state, started, now)
		_ = cmds // no dispatch loop exists yet (L05); the fold is only to resolve the graph
		if err != nil {
			writeError(w, http.StatusInternalServerError, "invariant_violation", "folding run.started: "+err.Error())
			return
		}
		if _, err := deps.Store.AppendIf(r.Context(), runID, 1, []domain.Event{started}, meta); err != nil {
			writeError(w, http.StatusInternalServerError, "invariant_violation", "appending run.started: "+err.Error())
			return
		}

		writeJSON(w, http.StatusCreated, createRunResponse{RunID: runID, Status: string(state.Status)})
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
