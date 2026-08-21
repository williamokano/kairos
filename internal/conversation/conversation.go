// Package conversation is the read/append primitive for a Conversation's
// own event stream (stream_id = eventstore.ConversationStreamID(runID)).
// It exists so internal/api (a leaf — AGENTS.md §2: "nothing imports
// internal/api", the reverse also holds in spirit: api stays thin and
// does not need internal/engine) and internal/engine can share the exact
// same append/read logic without either depending on the other. L14
// scopes Conversation 1:1 with Run — see L14-conversations.md's
// Documented decisions.
package conversation

import (
	"context"
	"errors"
	"fmt"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/eventstore"
)

// AppendMessage appends one message to runID's Conversation stream,
// retrying on a CAS conflict — concurrent posts (two humans, or a post
// racing a reconciliation catch-up read) are expected, not exceptional.
// maxRetries is deliberately much higher than internal/engine's
// appendNext (5): that helper serializes writes a single shard goroutine
// already orders, so a conflict there means a rare cross-goroutine race;
// this one is a human-facing write path with no such ordering guarantee
// upstream (two composers, a retried HTTP client, a burst of replies),
// and losing a typed message to an exhausted retry budget is a worse
// failure mode than the extra, still-cheap, re-reads cost.
func AppendMessage(ctx context.Context, store eventstore.Store, runID, role, text string) error {
	return AppendMessageAs(ctx, store, runID, role, text, "")
}

// AppendMessageAs is AppendMessage plus an attributed author (a display
// name/username — see internal/identity — never a login identity; "no
// authorization at the moment" per the user's own framing). Empty author
// behaves exactly like AppendMessage.
func AppendMessageAs(ctx context.Context, store eventstore.Store, runID, role, text, author string) error {
	streamID := eventstore.ConversationStreamID(runID)
	const maxRetries = 50
	for i := 0; i < maxRetries; i++ {
		envs, err := store.Read(ctx, streamID)
		if err != nil {
			return fmt.Errorf("reading conversation %s: %w", runID, err)
		}
		_, err = store.AppendIf(ctx, streamID, len(envs), []domain.Event{
			domain.ConversationMessageAppended{Role: role, Text: text, Author: author},
		}, eventstore.AppendMeta{Actor: role, CorrelationID: runID})
		if err == nil {
			return nil
		}
		if errors.Is(err, eventstore.ErrConflict) {
			continue
		}
		return fmt.Errorf("appending conversation message for %s: %w", runID, err)
	}
	return fmt.Errorf("appending conversation message for %s: exhausted retries on conflict", runID)
}

// Messages returns every message on runID's Conversation stream, oldest
// first.
func Messages(ctx context.Context, store eventstore.Store, runID string) ([]domain.ConversationMessageAppended, error) {
	envs, err := store.Read(ctx, eventstore.ConversationStreamID(runID))
	if err != nil {
		return nil, fmt.Errorf("reading conversation %s: %w", runID, err)
	}
	out := make([]domain.ConversationMessageAppended, 0, len(envs))
	for _, env := range envs {
		msg, ok := env.Event.(domain.ConversationMessageAppended)
		if !ok {
			return nil, fmt.Errorf("conversation stream %s: event %T is not ConversationMessageAppended", runID, env.Event)
		}
		out = append(out, msg)
	}
	return out, nil
}
