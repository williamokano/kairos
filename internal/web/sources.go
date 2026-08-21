package web

import (
	"context"
	"net/http"

	"github.com/williamokano/kairos/internal/cli"
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
	Sources  []sourceRow
	FetchErr error
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
			renderPage(w, "sources", "sources", sourcesPageData{FetchErr: err})
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
		renderPage(w, "sources", "sources", sourcesPageData{Sources: rows})
	}
}
