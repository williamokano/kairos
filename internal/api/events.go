package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/williamokano/kairos/internal/events"
)

func registerEventRoutes(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("GET /events", handleEvents(deps))
}

// handleEvents is the SSE endpoint. Decision #8 in L04-daemon-api-cli.md:
// it subscribes to the live bus BEFORE replaying history via ReadAll, then
// drains the live channel dropping anything with GlobalSeq <= the highest
// sequence the replay already consumed — closing the gap/duplicate window
// between "history" and "live" without a lock. ?after= (or the
// Last-Event-ID header, which EventSource sets automatically on
// reconnect) names where to resume from; ?stream= filters to one run.
func handleEvents(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, http.StatusInternalServerError, "invariant_violation", "response does not support streaming")
			return
		}

		streamFilter := r.URL.Query().Get("stream")
		after := int64(0)
		if v := r.Header.Get("Last-Event-ID"); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				after = n
			}
		} else if v := r.URL.Query().Get("after"); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				after = n
			}
		}

		ctx := r.Context()
		ch, unsub := deps.Store.Subscribe(ctx)
		defer unsub()

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

		lastSent := after
		for {
			batch, err := deps.Store.ReadAll(ctx, lastSent, 100)
			if err != nil || len(batch) == 0 {
				break
			}
			for _, env := range batch {
				lastSent = env.GlobalSeq
				if streamFilter != "" && env.StreamID != streamFilter {
					continue
				}
				writeSSEFrame(w, env)
			}
			flusher.Flush()
		}

		for {
			select {
			case <-ctx.Done():
				return
			case env, ok := <-ch:
				if !ok {
					return
				}
				if env.GlobalSeq <= lastSent {
					continue
				}
				lastSent = env.GlobalSeq
				if streamFilter != "" && env.StreamID != streamFilter {
					continue
				}
				writeSSEFrame(w, env)
				flusher.Flush()
			}
		}
	}
}

func writeSSEFrame(w http.ResponseWriter, env events.Envelope) {
	body, err := json.Marshal(env)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "id: %d\ndata: %s\n\n", env.GlobalSeq, body)
}
