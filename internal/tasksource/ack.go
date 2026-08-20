package tasksource

import (
	"context"
	"fmt"
	"time"

	"github.com/williamokano/kairos/internal/eventstore"
)

// Ack calls src.Ack, but only the first time for a given IdempotencyKey —
// 08-triggers.md: "ack routes through the effect manager... because a
// duplicated 'I've started work on this' comment is precisely the debris
// effects exist to prevent."
//
// Documented decision (see L16-triggers.md): this reuses trigger_dedupe's
// INSERT-ON-CONFLICT-DO-NOTHING primitive under an "ack:" key prefix,
// rather than internal/effect's Provider interface. effect.Provider is
// scoped to a RunID/NodeID/ExecID triple (an in-run node execution); an
// ack call happens outside any node execution — often before a run even
// exists (the poll-then-ack cycle that reports a rejection). The
// idempotency *shape* effects use (probe-before-act, replay returns the
// recorded result without acting) is preserved; the specific machinery is
// the simpler primitive that already fits this call site.
func Ack(ctx context.Context, store eventstore.Store, src Source, in AckInput) (AckOutput, error) {
	if in.IdempotencyKey == "" {
		return AckOutput{}, fmt.Errorf("tasksource: ack requires an idempotencyKey")
	}
	dedupeKey := "ack:" + in.IdempotencyKey
	_, isNew, err := store.DedupeTrigger(ctx, dedupeKey, "", in.ItemID, time.Now().Add(dedupeTTL))
	if err != nil {
		return AckOutput{}, fmt.Errorf("deduping ack: %w", err)
	}
	if !isNew {
		// Already acked — replay the recorded "yes" without calling out
		// again, matching the doc's crash-between-send-and-receive
		// concern.
		return AckOutput{}, nil
	}
	out, err := src.Ack(ctx, in)
	if err != nil {
		return AckOutput{}, err
	}
	if err := store.RecordTriggerRun(ctx, dedupeKey, "acked"); err != nil {
		return AckOutput{}, fmt.Errorf("recording ack: %w", err)
	}
	return out, nil
}
