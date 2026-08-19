package api

import "net/http"

type doctorResponse struct {
	Checks   []DoctorCheck `json:"checks"`
	Deferred []string      `json:"deferred"`
}

// registerDoctorRoute serves the checks cmd/kairos's serve bootstrap
// computed once at boot (Deps.DoctorChecks) — this handler never calls
// exec.LookPath itself; see L04-daemon-api-cli.md's decision #6.
func registerDoctorRoute(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("GET /doctor", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, doctorResponse{
			Checks:   deps.DoctorChecks,
			Deferred: deps.Deferred,
		})
	})
}
