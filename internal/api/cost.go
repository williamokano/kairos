package api

import (
	"net/http"
	"time"
)

// costResponse is the daemon's one real spend-adjacent number: L07's
// admission daily-spend cap tracker, which is an ESTIMATE checked at
// admission time against a node's declared resources.model.maxCostUSD,
// never a reconciliation against what a run actually cost — that number
// is never known (NL-30, 11-limitations.md). This handler does not
// pretend otherwise: it returns exactly the persisted estimate and the
// configured cap, nothing more.
type costResponse struct {
	Day           string  `json:"day"`
	SpentUSD      float64 `json:"spentUsd"`
	SpendRecorded bool    `json:"spendRecorded"`
	DailyCapUSD   float64 `json:"dailyCapUsd"`
}

// registerCostRoute backs `kairos cost` / the web `/cost` page — the first
// time this daemon has ever exposed L07's admission_spend table to a
// client. Before this, the running total existed only inside
// internal/admission.Manager's own process memory (persisted for restart
// survival, per L07's Future work) and was never read back by anything a
// human could see.
func registerCostRoute(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("GET /cost", func(w http.ResponseWriter, r *http.Request) {
		// Local calendar date, matching internal/admission's own dayKey
		// convention exactly (02-config.md: "your card", a single-user
		// tool, not a fleet spanning timezones) — duplicated here rather
		// than importing internal/admission for one format string, since
		// this handler only ever reads the store directly.
		day := time.Now().Local().Format("2006-01-02")
		spent, ok, err := deps.Store.GetAdmissionSpend(r.Context(), day)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "invariant_violation", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, costResponse{
			Day: day, SpentUSD: spent, SpendRecorded: ok, DailyCapUSD: deps.DailyUSD,
		})
	})
}
