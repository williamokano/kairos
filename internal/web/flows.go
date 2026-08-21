package web

import (
	"context"
	"encoding/json"
	"net/http"
)

// flowRow is one distinct DefinitionRef this daemon has actually run —
// the honest substitute for "published workflow definitions": there is
// no definitions table anywhere in this system (registry.Load reads a
// YAML file straight off disk at run-creation time, every time; nothing
// durable ever calls a flow "published"). What IS real and durable is
// every run's own TriggerReceived.DefinitionRef, so this page groups by
// that instead of inventing a publish step that does not exist.
type flowRow struct {
	DefinitionRef     string
	Runs              int
	Succeeded, Failed int
	RunIDs            []string
}

type flowsPageData struct {
	Flows    []flowRow
	FetchErr error
}

func handleFlowsPage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()

		envs, err := fetchAllEvents(ctx, deps.Client)
		if err != nil {
			renderPage(w, "flows", "flows", flowsPageData{FetchErr: err})
			return
		}

		statusByRun := map[string]string{}
		if runs, err := deps.Client.ListRuns(ctx, ""); err == nil {
			for _, run := range runs {
				statusByRun[run.RunID] = run.Status
			}
		}

		// Walked in envs' own GlobalSeq order (not a map range, which Go
		// deliberately randomizes) so the flow list's order — and which
		// run counts as "first seen" per definition — is stable across
		// requests rather than reshuffling on every page load.
		byDef := map[string]*flowRow{}
		var order []string
		for _, e := range envs {
			if e.EventType != "trigger.received" {
				continue
			}
			var p struct{ RunID, DefinitionRef string }
			if json.Unmarshal(e.Event, &p) != nil || p.DefinitionRef == "" {
				continue
			}
			row, ok := byDef[p.DefinitionRef]
			if !ok {
				row = &flowRow{DefinitionRef: p.DefinitionRef}
				byDef[p.DefinitionRef] = row
				order = append(order, p.DefinitionRef)
			}
			row.Runs++
			row.RunIDs = append(row.RunIDs, p.RunID)
			switch statusByRun[p.RunID] {
			case "succeeded":
				row.Succeeded++
			case "failed":
				row.Failed++
			}
		}

		data := flowsPageData{}
		for _, def := range order {
			data.Flows = append(data.Flows, *byDef[def])
		}
		renderPage(w, "flows", "flows", data)
	}
}
