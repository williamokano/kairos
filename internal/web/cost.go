package web

import (
	"context"
	"net/http"
)

type costPageData struct {
	Cost               costPageCost
	FetchErr           error
	UnknownCostCount   int
	UnknownCostFetched bool
}

type costPageCost struct {
	Day           string
	SpentUSD      float64
	SpendRecorded bool
	DailyCapUSD   float64
}

// handleCostPage is 10-webui.md's `/cost`, scoped honestly to what this
// system actually knows (NL-30, 11-limitations.md: "cost accounting is
// always unknown for llm-actor nodes"). It does NOT render the mockup's
// "spend by day / project / model / actor" breakdown — that data does
// not exist; GetAdmissionSpend keeps exactly one running total (today's
// calendar day, an admission-time ESTIMATE), with no history table
// behind it. What this page shows is real: today's estimate against the
// configured cap, plus a count of executions whose actual cost is
// recorded as flatly unknown — the honest complement to the estimate,
// not a chart this system cannot produce.
func handleCostPage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()

		resp, err := deps.Client.Cost(ctx)
		data := costPageData{}
		if err != nil {
			data.FetchErr = err
			renderPage(w, "cost", "cost", data)
			return
		}
		data.Cost = costPageCost{
			Day: resp.Day, SpentUSD: resp.SpentUSD, SpendRecorded: resp.SpendRecorded, DailyCapUSD: resp.DailyCapUSD,
		}

		if envs, err := fetchAllEvents(ctx, deps.Client); err == nil {
			data.UnknownCostFetched = true
			for _, e := range envs {
				if e.EventType == "session.cost.unavailable" {
					data.UnknownCostCount++
				}
			}
		}

		renderPage(w, "cost", "cost", data)
	}
}
