package eventstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/eventstore"
)

// TestStore_verifyAndRebuildSkipTheSystemStream is a regression test:
// domain.Advance has no case for L05's system-stream events
// (EngineStarted et al.), so Verify/Rebuild must skip stream_id="system"
// entirely rather than trying to fold them like a run's events.
func TestStore_verifyAndRebuildSkipTheSystemStream(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	meta := eventstore.AppendMeta{Actor: "engine", CorrelationID: "system", OccurredAt: time.Unix(0, 0)}

	if _, err := st.AppendIf(ctx, eventstore.SystemStream, 0, []domain.Event{
		domain.EngineStarted{BootID: "b1"},
	}, meta); err != nil {
		t.Fatalf("AppendIf: %v", err)
	}
	if _, err := st.AppendIf(ctx, eventstore.SystemStream, 1, []domain.Event{
		domain.EngineReconciled{},
	}, meta); err != nil {
		t.Fatalf("AppendIf: %v", err)
	}

	if _, err := st.Verify(ctx); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if err := st.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
}
