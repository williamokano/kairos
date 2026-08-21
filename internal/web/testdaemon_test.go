package web_test

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/williamokano/kairos/internal/cli"
	"github.com/williamokano/kairos/internal/web"
)

// fakeDaemon is a hand-rolled stand-in for the real daemon's admin socket
// — internal/web must never import internal/api (it is a pure client, see
// server.go's package doc), so these tests fake the handful of JSON
// endpoints cli.Client actually calls rather than spinning up the real
// daemon. cmd/kairos's own end-to-end tests (kill_mid_run_test.go's
// established pattern) exercise the real thing; these tests exercise
// internal/web's own logic — auth, rendering, route shape — in isolation.
type fakeDaemon struct {
	runs            []cli.RunSummary
	runState        cli.RunState
	events          []cli.Envelope
	messages        []cli.ConversationMessage
	approveCalls    []approveCall
	approveErr      error
	humanTasks      []cli.OpenHumanTask
	diffResult      cli.DiffResult
	diffErr         error
	compareResult   cli.CompareResult
	compareErr      error
	sources         []cli.Source
	costResp        cli.CostResponse
	cancelCalls     []cancelCall
	cancelErr       error
	forkCalls       []forkCall
	forkErr         error
	forkResult      cli.ForkResult
	pauseCalls      []string
	pauseErr        error
	createRunErr    error
	doCalls         []doCall
	doErr           error
	doResult        cli.DoResponse
	sessions        map[string]cli.Session
	sessionsErr     error
	projects        []cli.Project
	endSessionCalls []endSessionCall
	endSessionErr   error
}

type doCall struct{ Text, ContinueRunID, SessionID string }
type endSessionCall struct{ ID, Reason, Confirm string }

type cancelCall struct{ RunID, Reason string }
type forkCall struct {
	RunID      string
	AtSequence int
	AllowDrift bool
}

type approveCall struct{ RunID, NodeID, Decision, Reason, TypedWord string }

func newFakeDaemon(t *testing.T) (*fakeDaemon, string) {
	t.Helper()
	fd := &fakeDaemon{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /runs", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"runs": fd.runs})
	})
	mux.HandleFunc("GET /runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(fd.runState)
	})
	mux.HandleFunc("GET /human-tasks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(fd.humanTasks)
	})
	mux.HandleFunc("POST /runs", func(w http.ResponseWriter, r *http.Request) {
		if fd.createRunErr != nil {
			// Matches the real daemon's internal/api/respond.go envelope
			// shape ({"error":{"code","message"}}) — a plain-text
			// http.Error body here would be unrealistic and silently
			// undertest internal/cli.Client's real error-decoding path.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]string{"code": "validation_failed", "message": fd.createRunErr.Error()},
			})
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(cli.CreateRunResponse{RunID: "run_fake", Status: "running"})
	})
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range fd.events {
			b, _ := json.Marshal(e)
			_, _ = w.Write([]byte("data: " + string(b) + "\n\n"))
		}
	})
	mux.HandleFunc("GET /runs/{id}/conversation", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"messages": fd.messages})
	})
	mux.HandleFunc("POST /runs/{id}/conversation/messages", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /do", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Text          string
			ContinueRunID string `json:"continueRunId"`
			SessionID     string `json:"sessionId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		fd.doCalls = append(fd.doCalls, doCall{req.Text, req.ContinueRunID, req.SessionID})
		if fd.doErr != nil {
			http.Error(w, fd.doErr.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(fd.doResult)
	})
	mux.HandleFunc("GET /sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		s, ok := fd.sessions[r.PathValue("id")]
		if fd.sessionsErr != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "invariant_violation", "message": fd.sessionsErr.Error()}})
			return
		}
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "not_found", "message": "no such session"}})
			return
		}
		_ = json.NewEncoder(w).Encode(s)
	})
	mux.HandleFunc("DELETE /sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Reason, Confirm string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		fd.endSessionCalls = append(fd.endSessionCalls, endSessionCall{r.PathValue("id"), req.Reason, req.Confirm})
		if req.Confirm != r.PathValue("id") {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "validation_failed", "message": "confirm must match"}})
			return
		}
		if fd.endSessionErr != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "invariant_violation", "message": fd.endSessionErr.Error()}})
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ended"})
	})
	mux.HandleFunc("GET /sessions", func(w http.ResponseWriter, r *http.Request) {
		out := make([]cli.Session, 0, len(fd.sessions))
		for _, s := range fd.sessions {
			out = append(out, s)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sessions": out})
	})
	mux.HandleFunc("GET /projects", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"projects": fd.projects})
	})
	mux.HandleFunc("POST /runs/{id}/approve", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ NodeID, Decision, Reason, TypedWord string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		fd.approveCalls = append(fd.approveCalls, approveCall{r.PathValue("id"), req.NodeID, req.Decision, req.Reason, req.TypedWord})
		if fd.approveErr != nil {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "invariant_violation", "message": fd.approveErr.Error()}})
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /doctor", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(cli.DoctorResponse{})
	})
	mux.HandleFunc("GET /runs/{id}/diff", func(w http.ResponseWriter, r *http.Request) {
		if fd.diffErr != nil {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "not_found", "message": fd.diffErr.Error()}})
			return
		}
		_ = json.NewEncoder(w).Encode(fd.diffResult)
	})
	mux.HandleFunc("GET /sources", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"sources": fd.sources})
	})
	mux.HandleFunc("GET /cost", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(fd.costResp)
	})
	mux.HandleFunc("POST /runs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Reason string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		fd.cancelCalls = append(fd.cancelCalls, cancelCall{r.PathValue("id"), req.Reason})
		if fd.cancelErr != nil {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "invariant_violation", "message": fd.cancelErr.Error()}})
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /runs/{id}/fork", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			AtSequence int
			AllowDrift bool
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		fd.forkCalls = append(fd.forkCalls, forkCall{r.PathValue("id"), req.AtSequence, req.AllowDrift})
		if fd.forkErr != nil {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "invariant_violation", "message": fd.forkErr.Error()}})
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(fd.forkResult)
	})
	mux.HandleFunc("POST /sources/{id}/pause", func(w http.ResponseWriter, r *http.Request) {
		fd.pauseCalls = append(fd.pauseCalls, r.PathValue("id"))
		if fd.pauseErr != nil {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "invariant_violation", "message": fd.pauseErr.Error()}})
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /runs/{a}/compare/{b}", func(w http.ResponseWriter, r *http.Request) {
		if fd.compareErr != nil {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "not_found", "message": fd.compareErr.Error()}})
			return
		}
		_ = json.NewEncoder(w).Encode(fd.compareResult)
	})

	sockPath := filepath.Join(t.TempDir(), "fake.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listening on fake unix socket: %v", err)
	}
	srv := httptest.NewUnstartedServer(mux)
	_ = srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)

	return fd, sockPath
}

func testDeps(t *testing.T, fd *fakeDaemon, sockPath string) web.Deps {
	t.Helper()
	return web.Deps{
		Client:       cli.NewClient(sockPath),
		SockPath:     sockPath,
		Token:        "test-token-0123456789abcdef0123456789abcdef",
		AllowedHosts: []string{"kairos.test"},
	}
}
