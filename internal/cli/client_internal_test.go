package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestClient_FollowEvents_reconnectsAndResumesWithoutGapOrDuplicate proves
// FollowEvents's whole reason to exist: a connection that gets cut
// mid-stream is retried, and the retry resumes from afterSeq rather than
// replaying or skipping anything. The fake server deliberately drops the
// first connection after one envelope, then asserts the reconnect's
// ?after= names exactly that envelope's GlobalSeq — the same resumption
// contract GET /events implements server-side via Last-Event-ID (ADR
// 0010), exercised here from the client's reconnect loop instead of a real
// daemon, so the retry/backoff/resume logic is pinned down deterministically.
func TestClient_FollowEvents_reconnectsAndResumesWithoutGapOrDuplicate(t *testing.T) {
	var mu sync.Mutex
	attempt := 0
	var gotAfterOnAttempt []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempt++
		this := attempt
		mu.Unlock()

		after := r.URL.Query().Get("after")
		mu.Lock()
		gotAfterOnAttempt = append(gotAfterOnAttempt, after)
		mu.Unlock()

		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("httptest ResponseWriter does not support http.Flusher")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		switch this {
		case 1:
			// First connection: send envelope 1, then drop — simulating a
			// transient disconnect mid-stream, before any live event ever
			// arrives.
			_, _ = fmt.Fprintf(w, "id: 1\ndata: {\"StreamID\":\"r1\",\"Sequence\":1,\"GlobalSeq\":1,\"EventType\":\"a\",\"Event\":{}}\n\n")
			flusher.Flush()
			return
		default:
			// Every reconnect after that: send envelope 2 and then hold
			// the connection open (as the real live tail would) until the
			// client gives up (ctx cancelled), rather than dropping again
			// — one clean reconnect is what this test asserts.
			_, _ = fmt.Fprintf(w, "id: 2\ndata: {\"StreamID\":\"r1\",\"Sequence\":2,\"GlobalSeq\":2,\"EventType\":\"b\",\"Event\":{}}\n\n")
			flusher.Flush()
			<-r.Context().Done()
			return
		}
	}))
	defer srv.Close()

	client := &Client{http: srv.Client(), base: srv.URL}

	var evMu sync.Mutex
	var globalSeqs []int64
	var statuses []FollowStatus
	got2 := make(chan struct{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- client.FollowEvents(ctx, "", 0, func(env Envelope) {
			evMu.Lock()
			globalSeqs = append(globalSeqs, env.GlobalSeq)
			n := len(globalSeqs)
			evMu.Unlock()
			if n == 2 {
				close(got2)
			}
		}, func(status FollowStatus) {
			evMu.Lock()
			statuses = append(statuses, status)
			evMu.Unlock()
		})
	}()

	select {
	case <-got2:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for both envelopes across the reconnect")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("FollowEvents did not return after ctx cancellation")
	}

	evMu.Lock()
	defer evMu.Unlock()

	if len(globalSeqs) != 2 || globalSeqs[0] != 1 || globalSeqs[1] != 2 {
		t.Fatalf("GlobalSeq sequence = %v, want [1 2] (no gap, no duplicate)", globalSeqs)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(gotAfterOnAttempt) < 2 {
		t.Fatalf("expected at least 2 connection attempts, got %d", len(gotAfterOnAttempt))
	}
	if gotAfterOnAttempt[0] != "0" {
		t.Errorf("first attempt's after= = %q, want %q", gotAfterOnAttempt[0], "0")
	}
	if gotAfterOnAttempt[1] != "1" {
		t.Errorf("reconnect's after= = %q, want %q (must resume from envelope 1, not replay or skip)", gotAfterOnAttempt[1], "1")
	}

	sawDisconnected := false
	sawConnected := false
	for _, s := range statuses {
		if s == FollowDisconnected {
			sawDisconnected = true
		}
		if s == FollowConnected {
			sawConnected = true
		}
	}
	if !sawDisconnected {
		t.Error("expected at least one FollowDisconnected status transition across the drop+reconnect")
	}
	if !sawConnected {
		t.Error("expected at least one FollowConnected status transition")
	}
}

// TestClient_StreamEvents_stopsOnContextCancellation guards the other half
// of FollowEvents's contract: StreamEvents itself must return promptly
// once ctx is cancelled, rather than blocking forever on a still-open
// connection — the property FollowEvents's own loop relies on to exit
// cleanly on Ctrl-C (kairos logs --follow) or TUI detach.
func TestClient_StreamEvents_stopsOnContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		<-r.Context().Done() // held open, exactly like a real idle live tail
	}))
	defer srv.Close()

	client := &Client{http: srv.Client(), base: srv.URL}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- client.StreamEvents(ctx, "", 0, nil, func(Envelope) error { return nil })
	}()

	time.Sleep(50 * time.Millisecond) // let the connection actually establish
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Error("expected a non-nil error (context cancelled) from StreamEvents, got nil")
		} else if !strings.Contains(err.Error(), "context canceled") {
			t.Errorf("StreamEvents error = %v, want it to mention context cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StreamEvents did not return within 5s of ctx cancellation")
	}
}
