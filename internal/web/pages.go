package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"
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
	mux.HandleFunc("GET /runs/{id}/diff", handleDiffPage(deps))
	mux.HandleFunc("GET /compare", handleComparePage(deps))
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
		}

		// "Waiting on you" now reads HumanTaskIndexProjection's real,
		// indexed table (internal/api's GET /human-tasks) instead of
		// scanning every non-terminal run's own state — the O(active
		// runs)-per-page-load gap L20-webui.md's Documented decision #5
		// named is closed.
		if tasks, err := deps.Client.ListOpenHumanTasks(ctx); err == nil {
			for _, t := range tasks {
				data.Waiting = append(data.Waiting, waitingItem{RunID: t.RunID, NodeID: t.NodeID})
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

// diffPageData is what diff.gohtml renders — the diff viewer
// (L20-webui.md's Future work, built here): a node's before/after change
// (?node=) or, with no node, the whole run's change against the project's
// configured base ref.
type diffPageData struct {
	RunID, NodeID, Mode             string
	FromRef, ToRef                  string
	Files                           []diffFileView
	TotalAdded, TotalRemoved        int
	ScopeViolations, WorkspacePaths []string
}

// handleDiffPage implements GET /runs/{id}/diff. mode defaults to "split"
// (side-by-side, matching 10-webui.md's own mockup) and "unified" is the
// only other accepted value. A ?file=&line= deep link is resolved once
// server-side into a same-page 302 whose Location carries a URL fragment
// (#f{n}-p{n}) — a plain browser jumps to the matching id on load with no
// client script at all, which this page's strict CSP (script-src 'self',
// no inline) would block anyway.
func handleDiffPage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID := r.PathValue("id")
		nodeID := r.URL.Query().Get("node")
		mode := "split"
		if r.URL.Query().Get("mode") == "unified" {
			mode = "unified"
		}

		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		result, err := deps.Client.Diff(ctx, runID, nodeID)
		if err != nil {
			http.Error(w, "loading diff: "+err.Error(), http.StatusBadGateway)
			return
		}

		files := buildDiffFileViews(result.Files, result.Patch, result.ScopeViolations)

		if wantFile := r.URL.Query().Get("file"); wantFile != "" {
			if anchor, ok := findDeepLinkAnchor(files, wantFile, r.URL.Query().Get("line")); ok {
				q := r.URL.Query()
				q.Del("file")
				q.Del("line")
				target := r.URL.Path
				if enc := q.Encode(); enc != "" {
					target += "?" + enc
				}
				http.Redirect(w, r, target+"#"+anchor, http.StatusFound)
				return
			}
		}

		var added, removed int
		for _, f := range files {
			added += f.Added
			removed += f.Removed
		}

		renderPage(w, runID, "diff", diffPageData{
			RunID: runID, NodeID: nodeID, Mode: mode,
			FromRef: result.FromRef, ToRef: result.ToRef,
			Files: files, TotalAdded: added, TotalRemoved: removed,
			ScopeViolations: result.ScopeViolations, WorkspacePaths: result.WorkspacePaths,
		})
	}
}

// findDeepLinkAnchor locates the line-level anchor id for a ?file=&line=
// deep link: an empty/unparsed line jumps to the file's first hunk line;
// otherwise it matches the new-file line number, falling back to the
// old-file number for a pure deletion (which has no new-file line at
// all).
func findDeepLinkAnchor(files []diffFileView, path, lineStr string) (string, bool) {
	line, _ := strconv.Atoi(lineStr)
	for _, f := range files {
		if f.Path != path {
			continue
		}
		if line == 0 {
			for _, h := range f.Hunks {
				if len(h.Lines) > 0 {
					return h.Lines[0].Anchor, true
				}
			}
			return "", false
		}
		for _, h := range f.Hunks {
			for _, l := range h.Lines {
				if l.NewNo == line || (l.NewNo == 0 && l.OldNo == line) {
					return l.Anchor, true
				}
			}
		}
		return "", false
	}
	return "", false
}

// handleComparePage implements /compare?a=&b= — 10-webui.md's two-run
// side-by-side, rendering internal/cli.Client.Compare's existing call
// (which itself calls L18's Engine.Compare) rather than recomputing
// anything: cost/duration/attempts/findings plus a fork-drift annotation
// when one side is a fork of the other. Cost is omitted, matching
// internal/api/compare.go's own documented reason (never durably
// metered, NL-30) — this page renders exactly what Compare returns, it
// does not add a field Compare itself doesn't have.
func handleComparePage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, b := r.URL.Query().Get("a"), r.URL.Query().Get("b")
		if a == "" || b == "" {
			http.Error(w, "compare requires both ?a= and ?b= run ids", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		result, err := deps.Client.Compare(ctx, a, b)
		if err != nil {
			http.Error(w, "comparing runs: "+err.Error(), http.StatusBadGateway)
			return
		}
		renderPage(w, a+" vs "+b, "compare", struct {
			A, B                 cli.CompareSide
			ADuration, BDuration time.Duration
		}{
			A: result.A, B: result.B,
			ADuration: time.Duration(result.A.Duration), BDuration: time.Duration(result.B.Duration),
		})
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
