package api

import (
	"encoding/json"
	"net/http"
)

// registerLifecycleRoutes serves L19's "close the lid" and self-check
// surface: pause/resume the engine's dispatch of new nodes, and an
// on-demand health check distinct from the boot-only Reconcile.
func registerLifecycleRoutes(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("POST /pause", func(w http.ResponseWriter, r *http.Request) {
		if deps.Engine == nil {
			writeError(w, http.StatusServiceUnavailable, "invariant_violation", "engine not configured")
			return
		}
		deps.Engine.SetPaused(r.Context(), true)
		writeJSON(w, http.StatusOK, map[string]bool{"paused": true})
	})

	mux.HandleFunc("POST /resume", func(w http.ResponseWriter, r *http.Request) {
		if deps.Engine == nil {
			writeError(w, http.StatusServiceUnavailable, "invariant_violation", "engine not configured")
			return
		}
		deps.Engine.SetPaused(r.Context(), false)
		writeJSON(w, http.StatusOK, map[string]bool{"paused": false})
	})

	mux.HandleFunc("POST /selfcheck", func(w http.ResponseWriter, r *http.Request) {
		if deps.Engine == nil {
			writeError(w, http.StatusServiceUnavailable, "invariant_violation", "engine not configured")
			return
		}
		report, err := deps.Engine.SelfCheck(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "invariant_violation", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, report)
	})
}

// registerBackupRoute is separate from registerDBRoutes (db.go, L02/L04)
// only because it needs a request body (destPath) unlike verify/rebuild —
// kept as its own file so db.go's existing shape doesn't need touching.
func registerBackupRoute(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("POST /db/backup", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed", "path is required")
			return
		}
		if err := deps.Store.Backup(r.Context(), req.Path); err != nil {
			writeError(w, http.StatusInternalServerError, "invariant_violation", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
}
