package web

import (
	"context"
	"html"
	"net/http"
	"strconv"

	"github.com/williamokano/kairos/internal/cli"
	"github.com/williamokano/kairos/internal/tasksource"
)

// sourceRow flattens cli.Source's optional pointer fields (LastPollAt/
// NextPollAt) to plain strings before the template sees them — not
// strictly required (html/template already treats a nil pointer as
// falsy in {{if}}), but it keeps sources.gohtml's rows plain strings
// throughout, matching every other row type this page's template funcs
// already expect.
type sourceRow struct {
	cli.Source
	LastPollAt, NextPollAt string
}

type sourcesPageData struct {
	Sources   []sourceRow
	FetchErr  error
	FormNonce string
}

// handleSourcesPage is 10-webui.md's `/sources` — L16's trigger sources
// read back "verbatim" (per-source health, last poll, cursor,
// consecutive errors), reusing the existing `GET /sources` route/`kairos
// src ls` verb rather than adding a new one. This pass is a read-only
// status view: the pause/resume mutation dialogs 10-webui.md also names
// for this page are explicitly out of scope here (see Future work) —
// the CLI path (`kairos src pause|resume`) is unaffected and already
// real.
func handleSourcesPage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		sources, err := deps.Client.ListSources(ctx)
		if err != nil {
			renderPage(w, "sources", "sources", sourcesPageData{FetchErr: err, FormNonce: nonce()})
			return
		}
		rows := make([]sourceRow, 0, len(sources))
		for _, s := range sources {
			row := sourceRow{Source: s}
			if s.LastPollAt != nil {
				row.LastPollAt = *s.LastPollAt
			}
			if s.NextPollAt != nil {
				row.NextPollAt = *s.NextPollAt
			}
			rows = append(rows, row)
		}
		renderPage(w, "sources", "sources", sourcesPageData{Sources: rows, FormNonce: nonce()})
	}
}

// handleCreateCronSource is the Sources page's real, structured-field
// form — 08-triggers.md's own named Future work ("a friendlier per-kind
// flag surface... is cosmetic, deferred"), closed here for "cron", the
// one source kind that's actually a real, constructible Source today
// (github/jira/linear/plugin are never registered anywhere in this tree
// — see internal/tasksource.BuildCronConfig's doc comment). Builds the
// IDENTICAL config string `kairos src add cron`'s friendly flags build,
// via the same shared tasksource.BuildCronConfig — never a second,
// divergent schema — then calls the same deps.Client.AddSource every
// other source-creation entrypoint already uses.
func handleCreateCronSource(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			writeFlowFormError(w, http.StatusBadRequest, "bad form: "+err.Error())
			return
		}
		id := r.PostForm.Get("id")
		schedule := r.PostForm.Get("schedule")
		flow := r.PostForm.Get("flow")
		if id == "" || schedule == "" || flow == "" {
			writeFlowFormError(w, http.StatusBadRequest, "id, schedule, and flow are all required")
			return
		}
		weekday, _ := strconv.Atoi(r.PostForm.Get("weekday"))
		hour, _ := strconv.Atoi(r.PostForm.Get("hour"))
		minute, _ := strconv.Atoi(r.PostForm.Get("minute"))
		config, err := tasksource.BuildCronConfig(schedule, weekday, hour, minute)
		if err != nil {
			writeFlowFormError(w, http.StatusBadRequest, err.Error())
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		src, err := deps.Client.AddSource(ctx, id, "cron", config, flow, "", 0)
		if err != nil {
			writeFlowFormError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<p class="mutation-done">source <code>` + html.EscapeString(src.ID) + `</code> created — <a href="/sources">back to sources</a></p>`))
	}
}
