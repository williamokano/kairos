package web

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

func registerFragments(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("GET /frag/run/{id}/timeline", handleRunTimelineFrag(deps))
	mux.HandleFunc("GET /frag/run/{id}/events", handleRunEventsSSE(deps))
}

func handleRunTimelineFrag(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		envs, err := deps.Client.Events(ctx, id, 500*time.Millisecond)
		if err != nil {
			http.Error(w, "loading events: "+err.Error(), http.StatusBadGateway)
			return
		}
		renderFragment(w, "frag/timeline", envs)
	}
}

// handleRunEventsSSE reverse-proxies the daemon's own `/events` SSE
// endpoint verbatim — the SAME Last-Event-ID/global_seq resumption
// discipline ADR 0010 requires (10-webui.md: "one resumption story, not
// three"), reused rather than reimplemented. It bypasses cli.Client (whose
// Events() is a bounded historical read, not a live stream) and dials the
// admin unix socket directly, because Go's net/http has no "give me the
// live chunked body" client method — httputil.ReverseProxy already handles
// flush-per-chunk correctly for SSE.
func handleRunEventsSSE(deps Deps) http.HandlerFunc {
	target := &url.URL{Scheme: "http", Host: "kairos"}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			id := pr.In.PathValue("id")
			q := url.Values{"stream": {id}}
			if last := pr.In.Header.Get("Last-Event-ID"); last != "" {
				q.Set("after", last)
			}
			pr.Out.URL.Path = "/events"
			pr.Out.URL.RawQuery = q.Encode()
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", deps.SockPath)
			},
		},
		FlushInterval: -1, // stream every write immediately — required for SSE
	}
	return proxy.ServeHTTP
}
