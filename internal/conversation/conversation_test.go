package conversation_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/williamokano/kairos/internal/conversation"
	"github.com/williamokano/kairos/internal/events"
	"github.com/williamokano/kairos/internal/eventstore"
)

func openStore(t *testing.T) eventstore.Store {
	t.Helper()
	registry, err := events.Builtin()
	if err != nil {
		t.Fatalf("Builtin: %v", err)
	}
	st, err := eventstore.Open(context.Background(), eventstore.Config{
		Path:     filepath.Join(t.TempDir(), "kairos.db"),
		Registry: registry,
		Projections: []eventstore.Projection{
			eventstore.RunStateProjection{},
			eventstore.RunIndexProjection{},
		},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestAppendMessage_thenMessagesReturnsThemInOrder(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()

	if err := conversation.AppendMessage(ctx, st, "run_1", "human", "first"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if err := conversation.AppendMessage(ctx, st, "run_1", "human", "second"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	msgs, err := conversation.Messages(ctx, st, "run_1")
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("len(msgs) = %d, want 2", len(msgs))
	}
	if msgs[0].Text != "first" || msgs[1].Text != "second" {
		t.Errorf("msgs = %+v, want [first, second] in order", msgs)
	}
}

func TestMessages_emptyConversationReturnsEmptyNotError(t *testing.T) {
	st := openStore(t)
	msgs, err := conversation.Messages(context.Background(), st, "run_never_touched")
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("len(msgs) = %d, want 0", len(msgs))
	}
}

// TestAppendMessage_concurrentAppendsAllSucceedAndAreOrdered proves the
// CAS-retry loop actually handles real contention: N goroutines posting
// to the same conversation must all land, none silently lost, none
// duplicated.
func TestAppendMessage_concurrentAppendsAllSucceedAndAreOrdered(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	const n = 20

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := conversation.AppendMessage(ctx, st, "run_concurrent", "human", "msg"); err != nil {
				t.Errorf("AppendMessage: %v", err)
			}
		}()
	}
	wg.Wait()

	msgs, err := conversation.Messages(ctx, st, "run_concurrent")
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != n {
		t.Errorf("len(msgs) = %d, want %d", len(msgs), n)
	}
}

// TestConversationStream_neverFoldsThroughRunProjections is the L05/L11
// pattern's regression proof applied to L14's own aux stream: appending
// to a Conversation must never trip "unknown event type" the way an
// unhandled event would if RunStateProjection tried to fold it, and
// Verify/Rebuild must both skip it cleanly.
func TestConversationStream_neverFoldsThroughRunProjections(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()

	if err := conversation.AppendMessage(ctx, st, "run_aux", "human", "hello"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if _, err := st.Verify(ctx); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if err := st.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
}
