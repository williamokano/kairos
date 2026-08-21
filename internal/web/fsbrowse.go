package web

import (
	"context"
	"net/http"

	"github.com/williamokano/kairos/internal/cli"
)

type fsBrowseFragData struct {
	cli.FSBrowseResponse
	FetchErr error
}

// registerFSBrowseFragment backs the Projects page's directory picker —
// the user's own ask: "some rich 'selector' that would list the path,
// nested, so I could select from there, not always I remember the path
// from the top of my head." A fragment, not a full page: clicking into a
// subdirectory re-fetches this same route with a new ?path=, swapping
// only the listing, and "use this path" fills the create-project form's
// text input via a one-line inline script (no new JS file needed for
// something this small — see app.js's own doc comment on keeping this
// codebase's hand-written JS budget narrow).
func registerFSBrowseFragment(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("GET /frag/fs/browse", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		resp, err := deps.Client.BrowseFS(ctx, r.URL.Query().Get("path"))
		if err != nil {
			renderFragment(w, "frag/fsbrowse", fsBrowseFragData{FetchErr: err})
			return
		}
		renderFragment(w, "frag/fsbrowse", fsBrowseFragData{FSBrowseResponse: resp})
	})
}
