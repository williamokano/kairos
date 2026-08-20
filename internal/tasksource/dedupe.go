package tasksource

import (
	"context"
	"fmt"
	"time"

	"github.com/williamokano/kairos/internal/eventstore"
)

// dedupeTTL is 08-triggers.md's "+30d" — how long a dedupe_key row
// survives before it could theoretically be reused (this package never
// prunes; that's Future work, see L16-triggers.md).
const dedupeTTL = 30 * 24 * time.Hour

// TriggerRun is DedupeTrigger + CreateRun composed correctly: claim the
// dedupe key FIRST (before the possibly-slow definition load and
// two-append sequence), then create the run, then record it — so two
// concurrent pollers racing the same dedupeKey never both create a run.
// Returns the run that now exists for dedupeKey (freshly created or
// already-existing) and whether this call was the one that created it.
func TriggerRun(ctx context.Context, store eventstore.Store, dedupeKey, sourceID, itemID string, req CreateRunRequest, limits QueueLimits) (runID string, created bool, err error) {
	existing, isNew, err := store.DedupeTrigger(ctx, dedupeKey, sourceID, itemID, time.Now().Add(dedupeTTL))
	if err != nil {
		return "", false, fmt.Errorf("deduping trigger: %w", err)
	}
	if !isNew {
		if existing != "" {
			return existing, false, nil
		}
		// A concurrent creator claimed the key but hasn't called
		// RecordTriggerRun yet — a narrow, real race this package
		// accepts rather than hides: report it distinctly so a caller
		// (poller/inbox) can retry the ack/report step, not silently
		// return an empty run id as if nothing happened.
		return "", false, fmt.Errorf("tasksource: dedupe key %s claimed concurrently, run not yet recorded", dedupeKey)
	}

	runID, _, err = CreateRun(ctx, store, req, limits)
	if err != nil {
		return "", false, err
	}
	if err := store.RecordTriggerRun(ctx, dedupeKey, runID); err != nil {
		return "", false, fmt.Errorf("recording trigger run: %w", err)
	}
	return runID, true, nil
}
