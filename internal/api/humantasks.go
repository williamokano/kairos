package api

import (
	"net/http"
)

func registerHumanTaskRoutes(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("GET /human-tasks", handleListHumanTasks(deps))
}

type openHumanTaskResponse struct {
	RunID    string `json:"runId"`
	NodeID   string `json:"nodeId"`
	Kind     string `json:"kind"`
	OpenedAt string `json:"openedAt"`
}

// handleListHumanTasks is L20-webui.md's Documented decision #5's fix: a
// real, indexed answer to "what's currently waiting on a human," reading
// HumanTaskIndexProjection's table rather than scanning every
// non-terminal run's own state. state=open is the only value this
// endpoint supports today — there is no history of answered/closed tasks
// to serve yet (that would need retaining closed rows rather than
// deleting them, a real design decision left for whenever something
// actually needs an answered-task history).
func handleListHumanTasks(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if state := r.URL.Query().Get("state"); state != "" && state != "open" {
			writeError(w, http.StatusBadRequest, "usage", "state must be \"open\" (the only value currently supported)")
			return
		}
		tasks, err := deps.Store.ListOpenHumanTasks(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "invariant_violation", err.Error())
			return
		}
		out := make([]openHumanTaskResponse, len(tasks))
		for i, t := range tasks {
			out[i] = openHumanTaskResponse{RunID: t.RunID, NodeID: t.NodeID, Kind: t.Kind, OpenedAt: t.OpenedAt}
		}
		writeJSON(w, http.StatusOK, out)
	}
}
