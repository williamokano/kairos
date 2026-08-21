package api

import (
	"net/http"

	"github.com/williamokano/kairos/internal/engine"
)

func registerDiffRoutes(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("GET /runs/{id}/diff", handleDiff(deps))
}

// diffFileResponse is one changed file's numstat summary, camelCase over
// the wire — internal/cli.DiffFile mirrors this shape, matching
// runSummaryForCompare/CompareSide's existing convention of an
// api-package response struct distinct from the engine's own Go-named
// fields.
type diffFileResponse struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Binary  bool   `json:"binary"`
}

type diffResponse struct {
	RunID           string             `json:"runId"`
	NodeID          string             `json:"nodeId,omitempty"`
	FromRef         string             `json:"fromRef"`
	ToRef           string             `json:"toRef"`
	Files           []diffFileResponse `json:"files"`
	Patch           string             `json:"patch"`
	WorkspacePaths  []string           `json:"workspacePaths,omitempty"`
	ScopeViolations []string           `json:"scopeViolations,omitempty"`
}

// handleDiff implements the diff viewer's GET /runs/{id}/diff
// (10-webui.md's route map) — an optional ?node= scopes it to one node's
// own before/after boundary; omitted, it's the whole run's change against
// the project's configured base ref. Every error (run/node not found, no
// snapshot recorded, no base ref configured) maps to 404, matching
// handleCompare's existing simplicity: this route has no state-mutating
// path where distinguishing 4xx causes would change client behaviour.
func handleDiff(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Engine == nil {
			writeError(w, http.StatusServiceUnavailable, "invariant_violation", "engine not configured")
			return
		}
		result, err := deps.Engine.Diff(r.Context(), engine.DiffRequest{
			RunID:  r.PathValue("id"),
			NodeID: r.URL.Query().Get("node"),
		})
		if err != nil {
			writeError(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		resp := diffResponse{
			RunID: result.RunID, NodeID: result.NodeID,
			FromRef: result.FromRef, ToRef: result.ToRef,
			Patch: result.Patch, WorkspacePaths: result.WorkspacePaths,
			ScopeViolations: result.ScopeViolations,
		}
		for _, f := range result.Files {
			resp.Files = append(resp.Files, diffFileResponse{Path: f.Path, Added: f.Added, Removed: f.Removed, Binary: f.Binary})
		}
		writeJSON(w, http.StatusOK, resp)
	}
}
