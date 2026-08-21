package web

import (
	"context"
	"encoding/json"
	"net/http"
)

// gateRow is one gate's effectiveness summary, per 10-webui.md's `/gates`
// ("fires per 100 runs, true-catch rate, waiver rate... a constraint that
// never fires is broken; one always waived is wrong") — computed from
// every real ConstraintEvaluated fact the gate has ever produced plus
// every WaiverGranted naming it, not from a static declared-gates list
// (declared gates live only in on-disk flow YAML the daemon never keeps
// a registry of — see the Flows page's own doc comment).
// Detail lives one click away at /findings?gate=<id> (its own
// filtered-table-plus-detail page), so this row carries only the
// aggregate a gate's own table row needs, not every individual
// evaluation.
type gateRow struct {
	GateID   string
	Kind     string
	Fires    int
	Failures int
	Waivers  int
}

// CatchRatePct is Failures/Fires as a whole percentage, computed here
// (not in the template) so gates.gohtml stays plain range/if — no new
// arithmetic template func for one page's one derived number.
func (g gateRow) CatchRatePct() int {
	if g.Fires == 0 {
		return 0
	}
	return g.Failures * 100 / g.Fires
}

type gatesPageData struct {
	GateFilter string
	Gates      []gateRow
	FetchErr   error
}

func handleGatesPage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		gateFilter := r.URL.Query().Get("gate")

		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		envs, err := fetchAllEvents(ctx, deps.Client)
		data := gatesPageData{GateFilter: gateFilter}
		if err != nil {
			data.FetchErr = err
			renderPage(w, "gates", "gates", data)
			return
		}

		byGate := map[string]*gateRow{}
		order := []string{}
		waivers := map[string]int{}
		for _, e := range envs {
			if e.EventType == "waiver.grant" {
				var p struct{ GateID string }
				if json.Unmarshal(e.Event, &p) == nil && p.GateID != "" {
					waivers[p.GateID]++
				}
				continue
			}
			row, ok := parseConstraintEvaluated(e)
			if !ok {
				continue
			}
			g, exists := byGate[row.GateID]
			if !exists {
				g = &gateRow{GateID: row.GateID, Kind: row.Kind}
				byGate[row.GateID] = g
				order = append(order, row.GateID)
			}
			g.Fires++
			if !row.Passed {
				g.Failures++
			}
		}

		for _, id := range order {
			if gateFilter != "" && id != gateFilter {
				continue
			}
			g := byGate[id]
			g.Waivers = waivers[id]
			data.Gates = append(data.Gates, *g)
		}

		renderPage(w, "gates", "gates", data)
	}
}
