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
	// Paused and InFlight are L19's park-mode surface: kairos park --wait
	// polls InFlight down to 0 to know when a paused daemon has genuinely
	// finished every node it was mid-executing.
	Paused   bool `json:"paused"`
	InFlight int  `json:"inFlight"`
}

func registerStatusRoute(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		running := domain.RunRunning
		runs, err := deps.Store.ListRuns(r.Context(), &running)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "invariant_violation", err.Error())
			return
		}
		resp := statusResponse{
			DaemonPID:  os.Getpid(),
			Uptime:     time.Since(deps.StartedAt).String(),
			ActiveRuns: len(runs),
		}
		if deps.Engine != nil {
			resp.Paused = deps.Engine.Paused()
			resp.InFlight = deps.Engine.InFlightCount()
		}
		writeJSON(w, http.StatusOK, resp)
	})
}
