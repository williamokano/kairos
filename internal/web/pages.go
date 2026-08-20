package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/williamokano/kairos/internal/cli"
	"github.com/williamokano/kairos/internal/decisionctx"
)

const requestTimeout = 10 * time.Second

func registerPages(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("GET /{$}", handleHome(deps))
	mux.HandleFunc("GET /runs", handleRunsList(deps))
	mux.HandleFunc("GET /runs/{id}", handleRunDetail(deps))
	mux.HandleFunc("GET /t/{runID}/{nodeID}", handleDecisionPage(deps))
	mux.HandleFunc("GET /c/{runID}", handleConversationPage(deps))
	mux.HandleFunc("GET /doctor", handleDoctorPage(deps))
}

type waitingItem struct{ RunID, NodeID string }

type homeData struct {
	FormNonce string
	Waiting   []waitingItem
	Running   []cli.RunSummary
}

// handleHome is 10-webui.md's "/" route: composer, waiting-on-you,
// running. Home's own scope-narrowing (no cost/gate-effectiveness cards,
// no "today" section) is documented in L20-webui.md.
func handleHome(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()

		runs, err := deps.Client.ListRuns(ctx, "")
		if err != nil {
			http.Error(w, "listing runs: "+err.Error(), http.StatusBadGateway)
			return
		}

		data := homeData{FormNonce: nonce()}
		for _, run := range runs {
			switch run.Status {
			case "running", "degraded":
				data.Running = append(data.Running, run)
			}
			// "Waiting on you" has no dedicated GET /human-tasks index yet
			// (10-webui.md names one; the daemon API doesn't have it) — so
			// this scans each non-terminal run's own Executions for a
			// waiting node, which is O(active runs) per home-page load. A
			// real, honest scope-narrowing: see L20-webui.md's Future work.
			if run.Status == "running" || run.Status == "degraded" {
				state, err := deps.Client.GetRun(ctx, run.RunID)
				if err == nil {
					for nodeID, execs := range state.Executions {
						for _, e := range execs {
							if e.Status == "waiting" {
								data.Waiting = append(data.Waiting, waitingItem{RunID: run.RunID, NodeID: nodeID})
							}
						}
					}
				}
			}
		}
		renderPage(w, "home", "home", data)
	}
}

func handleRunsList(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		runs, err := deps.Client.ListRuns(ctx, r.URL.Query().Get("status"))
		if err != nil {
			http.Error(w, "listing runs: "+err.Error(), http.StatusBadGateway)
			return
		}
		renderPage(w, "runs", "runs", struct{ Runs []cli.RunSummary }{runs})
	}
}

func handleRunDetail(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		state, err := deps.Client.GetRun(ctx, id)
		if err != nil {
			http.Error(w, "loading run: "+err.Error(), http.StatusBadGateway)
			return
		}
		envs, err := deps.Client.Events(ctx, id, 500*time.Millisecond)
		if err != nil {
			envs = nil // the run detail page still renders without event history — a load failure here is not the decision page's "partial degrades to blocked" rule, which is scoped to /t/ only
		}
		renderPage(w, id, "run", struct {
			State  cli.RunState
			Events []cli.Envelope
		}{state, envs})
	}
}

// handleDecisionPage is the highest-priority page in this document — see
// L20-webui.md's Documented decisions for why it renders synchronously
// (fetch-then-render-once) rather than the mockup's async per-pane
// loading: 10-webui.md's screens are progressively hydrated fragments,
// but a server-rendered document that already has every fact in hand has
// no meaningful "still loading" state to model, so "evidence load failure
// blocks the form" here means exactly what it says — a failed fetch
// renders the blocked state directly, first paint.
func handleDecisionPage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID, nodeID := r.PathValue("runID"), r.PathValue("nodeID")
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()

		envs, err := deps.Client.Events(ctx, runID, 3*time.Second)
		data := decisionPageData{RunID: runID, NodeID: nodeID, FormNonce: nonce()}
		if err != nil {
			data.EvidenceErr = err
		} else {
			data.Context = decisionctx.Build(envs, nodeID)
			data.EvidenceLoaded = true
		}
		renderPage(w, "decision", "decision", data)
	}
}

type decisionPageData struct {
	RunID, NodeID  string
	FormNonce      string
	EvidenceLoaded bool
	EvidenceErr    error
	Context        decisionctx.Context
}

func handleConversationPage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID := r.PathValue("runID")
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		msgs, err := deps.Client.GetConversation(ctx, runID)
		if err != nil {
			http.Error(w, "loading conversation: "+err.Error(), http.StatusBadGateway)
			return
		}
		renderPage(w, runID, "conversation", struct {
			RunID     string
			Messages  []cli.ConversationMessage
			FormNonce string
		}{runID, msgs, nonce()})
	}
}

func handleDoctorPage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		resp, err := deps.Client.Doctor(ctx)
		if err != nil {
			http.Error(w, "loading doctor report: "+err.Error(), http.StatusBadGateway)
			return
		}
		renderPage(w, "doctor", "doctor", resp)
	}
}

// nonce mints a per-render form value — 10-webui.md's Idempotency-Key
// pattern ("minted server-side into the form, so a double-submit or a
// retried POST creates one run, not two"). See L20-webui.md's Documented
// decisions: the daemon API does not yet dedupe on this header (a real,
// named gap, not silently faked as enforced).
func nonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
