package api

import (
	"net/http"
	"os"
	"time"

	"github.com/williamokano/kairos/internal/domain"
)

type statusResponse struct {
	DaemonPID int    `json:"daemonPid"`
	Uptime    string `json:"uptime"`
	// ActiveRuns is a placeholder count until L05's engine tracks live
	// dispatch; today it reflects runs whose folded status is "running"
	// (folded at write time by RunStateProjection/RunIndexProjection).
	ActiveRuns int `json:"activeRuns"`
}

func registerStatusRoute(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		running := domain.RunRunning
		runs, err := deps.Store.ListRuns(r.Context(), &running)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "invariant_violation", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, statusResponse{
			DaemonPID:  os.Getpid(),
			Uptime:     time.Since(deps.StartedAt).String(),
			ActiveRuns: len(runs),
		})
	})
}
