package web

import (
	"context"
	"net/http"

	"github.com/williamokano/kairos/internal/cli"
)

type runnersPageData struct {
	Doctor    cli.DoctorResponse
	DoctorErr error
}

// handleRunnersPage mirrors internal/tui's viewRunners exactly — L15's
// own doc comment: "a screen that shows one row is honest about there
// being one row." Real runner add/probe/drain management is
// 07-runners.md, a later, unbuilt phase; this page renders the one real
// `local` runner sourced from real `GET /doctor` data, not an invented
// per-runner listing no endpoint provides.
func handleRunnersPage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		resp, err := deps.Client.Doctor(ctx)
		data := runnersPageData{Doctor: resp, DoctorErr: err}
		renderPage(w, "runners", "runners", data)
	}
}
