package web

import (
	"context"
	"encoding/json"
	"html"
	"net/http"

	"github.com/williamokano/kairos/internal/cli"
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
	Flows     []flowRow
	Saved     []cli.FlowDefinition
	FetchErr  error
	FormNonce string
}

func handleFlowsPage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()

		saved, _ := deps.Client.ListFlowDefinitions(ctx) // a fetch failure here degrades to an empty list, not a blocked page — the historical-run table below is this page's primary content

		envs, err := fetchAllEvents(ctx, deps.Client)
		if err != nil {
			renderPage(w, "flows", "flows", flowsPageData{FetchErr: err, Saved: saved, FormNonce: nonce()})
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

		data := flowsPageData{Saved: saved, FormNonce: nonce()}
		for _, def := range order {
			data.Flows = append(data.Flows, *byDef[def])
		}
		renderPage(w, "flows", "flows", data)
	}
}

// writeFlowFormError renders an HTML fragment (not plain text) so htmx's
// app.js-level htmx:responseError listener (added for the composer's
// identical "click run and nothing happens" bug — see
// internal/web/mutations.go's writeComposerError) actually swaps it into
// the page on a non-2xx response, instead of the failure vanishing
// silently.
func writeFlowFormError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`<p class="error">` + html.EscapeString(msg) + `</p>`))
}

// handleCreateFlowDefinition backs the Flows page's "new flow" editor —
// posts straight to internal/api's POST /flow-definitions via
// deps.Client, so a bad workflow is rejected with the SAME real
// registry.Load error text `kairos flow create` would show, not a
// separate, possibly-diverging web-only message.
func handleCreateFlowDefinition(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			writeFlowFormError(w, http.StatusBadRequest, "bad form: "+err.Error())
			return
		}
		name := r.PostForm.Get("name")
		yamlText := r.PostForm.Get("yaml")
		if name == "" || yamlText == "" {
			writeFlowFormError(w, http.StatusBadRequest, "name and yaml are both required")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		flow, err := deps.Client.CreateFlowDefinition(ctx, name, yamlText)
		if err != nil {
			writeFlowFormError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<p class="mutation-done">saved: <code>` + html.EscapeString(flow.Path) + `</code> — <a href="/flows">back to flows</a></p>`))
	}
}
