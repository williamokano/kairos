package web

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/williamokano/kairos/internal/cli"
)

// findingRow is one domain.ConstraintEvaluated fact (L10/L11), parsed
// from its raw event JSON — the same event decisionctx.Build reads for
// one node's decision evidence, aggregated here across every run instead
// of one node.
type findingRow struct {
	Seq                   int64
	RunID, NodeID, ExecID string
	GateID, Kind          string
	Passed                bool
	ExitCode              int
	DurationMs            int64
	Reason                string
}

type findingsPageData struct {
	RunFilter, GateFilter, State string
	Rows                         []findingRow
	FetchErr                     error
}

// handleFindingsPage is 10-webui.md's `/findings` — "what is still open
// across every run" — a filtered table over every ConstraintEvaluated
// event the daemon has ever recorded, defaulting to failing evaluations
// only (state=all shows every evaluation, passing included). No new
// daemon route: this is computed entirely from GET /events, the same
// route the events explorer and `kairos logs` already use.
func handleFindingsPage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		runFilter := q.Get("run")
		gateFilter := q.Get("gate")
		state := q.Get("state")
		if state == "" {
			state = "failed"
		}

		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()

		var (
			envs []cli.Envelope
			err  error
		)
		if runFilter != "" {
			envs, err = deps.Client.Events(ctx, runFilter, fullLogTimeout)
		} else {
			envs, err = fetchAllEvents(ctx, deps.Client)
		}
		data := findingsPageData{RunFilter: runFilter, GateFilter: gateFilter, State: state}
		if err != nil {
			data.FetchErr = err
			renderPage(w, "findings", "findings", data)
			return
		}

		for _, e := range envs {
			row, ok := parseConstraintEvaluated(e)
			if !ok {
				continue
			}
			if gateFilter != "" && row.GateID != gateFilter {
				continue
			}
			if state == "failed" && row.Passed {
				continue
			}
			data.Rows = append(data.Rows, row)
		}

		renderPage(w, "findings", "findings", data)
	}
}

func parseConstraintEvaluated(e cli.Envelope) (findingRow, bool) {
	if e.EventType != "constraint.evaluated" {
		return findingRow{}, false
	}
	var p struct {
		RunID, NodeID, ExecID string
		GateID, Kind          string
		Passed                bool
		ExitCode              int
		DurationMs            int64
		Reason                string
	}
	if json.Unmarshal(e.Event, &p) != nil {
		return findingRow{}, false
	}
	return findingRow{
		Seq: e.GlobalSeq, RunID: p.RunID, NodeID: p.NodeID, ExecID: p.ExecID,
		GateID: p.GateID, Kind: p.Kind, Passed: p.Passed, ExitCode: p.ExitCode,
		DurationMs: p.DurationMs, Reason: p.Reason,
	}, true
}
