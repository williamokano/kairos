package api

import "net/http"

type dbVerifyResponse struct {
	MismatchedRunIDs []string `json:"mismatchedRunIds"`
}

func registerDBRoutes(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("POST /db/verify", func(w http.ResponseWriter, r *http.Request) {
		report, err := deps.Store.Verify(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "invariant_violation", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, dbVerifyResponse{MismatchedRunIDs: report.MismatchedRunIDs})
	})

	mux.HandleFunc("POST /db/rebuild", func(w http.ResponseWriter, r *http.Request) {
		if err := deps.Store.Rebuild(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "invariant_violation", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
}
