package web

import (
	"context"
	"net/http"
	"strconv"

	"github.com/williamokano/kairos/internal/cli"
)

// causalNode is one event log explorer's "causal tree" recursion step —
// L20-webui.md's Future work, built here: an indented graph from
// CausationSeq answering "why did it do that", the doc's own framing.
type causalNode struct {
	Env      cli.Envelope
	Children []*causalNode
}

type eventsPageData struct {
	StreamFilter, TypeFilter string
	AfterFilter, Focus       int64
	Rows                     []cli.Envelope
	Ancestry                 []cli.Envelope
	Descendants              []*causalNode
	FetchErr                 error
}

// handleEventsPage is 10-webui.md's event log explorer: a filtered table
// of the daemon's real event log (every stream, or one run via
// ?stream=), plus — for one event picked via ?focus= — its causal tree.
// The causal tree is computed from the FULL fetched set regardless of
// ?type=/?after=, since an ancestor or descendant of the focused event
// may itself sit outside whatever filter narrows the visible table rows;
// filters only ever decide what's shown in the table, never what the
// tree can see.
func handleEventsPage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		streamFilter := q.Get("stream")
		typeFilter := q.Get("type")
		after, _ := strconv.ParseInt(q.Get("after"), 10, 64)
		focus, _ := strconv.ParseInt(q.Get("focus"), 10, 64)

		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()

		all, err := deps.Client.Events(ctx, streamFilter, fullLogTimeout)
		data := eventsPageData{StreamFilter: streamFilter, TypeFilter: typeFilter, AfterFilter: after, Focus: focus}
		if err != nil {
			data.FetchErr = err
			renderPage(w, "events", "events", data)
			return
		}

		byGlobalSeq := make(map[int64]cli.Envelope, len(all))
		childrenOf := make(map[int64][]cli.Envelope)
		for _, e := range all {
			byGlobalSeq[e.GlobalSeq] = e
			if e.CausationSeq != nil {
				childrenOf[*e.CausationSeq] = append(childrenOf[*e.CausationSeq], e)
			}
		}
		for _, e := range all {
			if typeFilter != "" && e.EventType != typeFilter {
				continue
			}
			if after > 0 && e.GlobalSeq < after {
				continue
			}
			data.Rows = append(data.Rows, e)
		}

		if focus > 0 {
			if _, ok := byGlobalSeq[focus]; ok {
				data.Ancestry = ancestryChain(byGlobalSeq, focus)
				data.Descendants = descendantTree(childrenOf, focus, map[int64]bool{focus: true})
			}
		}

		renderPage(w, "events", "events", data)
	}
}

// ancestryChain walks CausationSeq from seq up to its ultimate root,
// returned root-first — the order a causal tree reads top to bottom:
// "what happened first."
func ancestryChain(byGlobalSeq map[int64]cli.Envelope, seq int64) []cli.Envelope {
	var chain []cli.Envelope
	seen := map[int64]bool{}
	cur := seq
	for {
		env, ok := byGlobalSeq[cur]
		if !ok || seen[cur] {
			break
		}
		seen[cur] = true
		chain = append(chain, env)
		if env.CausationSeq == nil {
			break
		}
		cur = *env.CausationSeq
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

// descendantTree recursively collects everything seq caused, directly or
// transitively. seen guards a malformed cycle — the event log's own
// invariants forbid one, but a rendering path must never infinite-loop on
// corrupt input.
func descendantTree(childrenOf map[int64][]cli.Envelope, seq int64, seen map[int64]bool) []*causalNode {
	var nodes []*causalNode
	for _, child := range childrenOf[seq] {
		if seen[child.GlobalSeq] {
			continue
		}
		seen[child.GlobalSeq] = true
		nodes = append(nodes, &causalNode{Env: child, Children: descendantTree(childrenOf, child.GlobalSeq, seen)})
	}
	return nodes
}
