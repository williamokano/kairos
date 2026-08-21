package web

import (
	"context"
	"time"

	"github.com/williamokano/kairos/internal/cli"
)

// fullLogTimeout bounds a cross-run event-log fetch — the events
// explorer, findings, gates, and flows pages all read the WHOLE log
// (every stream), not one run's, so they use a slightly longer quiet
// window than handleRunDetail/handleDecisionPage's per-run 500ms/3s
// before deciding history has been fully replayed. Still a real,
// documented O(entire event log) scan per page load — the same honest
// trade-off L20-webui.md's Documented decision #5 already accepted for
// the home page's "waiting on you" section, extended here rather than
// invented fresh: this is a single-user tool's event log, not a fleet's.
const fullLogTimeout = 1500 * time.Millisecond

// fetchAllEvents reads every envelope currently in the log — stream=""
// means every stream, matching cli.Client.Events's own documented
// behaviour, reused by every cross-run page in this package rather than
// each re-deciding the timeout for itself.
func fetchAllEvents(ctx context.Context, client *cli.Client) ([]cli.Envelope, error) {
	return client.Events(ctx, "", fullLogTimeout)
}
