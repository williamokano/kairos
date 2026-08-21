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
	runs          []cli.RunSummary
	runState      cli.RunState
	events        []cli.Envelope
	messages      []cli.ConversationMessage
	approveCalls  []approveCall
	approveErr    error
	humanTasks    []cli.OpenHumanTask
	diffResult    cli.DiffResult
	diffErr       error
	compareResult cli.CompareResult
	compareErr    error
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
