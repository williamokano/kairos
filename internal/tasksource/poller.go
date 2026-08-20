package tasksource

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"github.com/williamokano/kairos/internal/eventstore"
)

// PollerConfig configures one source's poll goroutine.
type PollerConfig struct {
	SourceID                    string
	Source                      Source
	Interval                    time.Duration
	DefaultFlow, DefaultProject string
	Volume                      VolumeConfig
	Limits                      QueueLimits
	Log                         *slog.Logger
}

// Poller runs one source's jittered poll loop: "next_poll_at = now +
// interval ± rand(0, interval/4)", exponential backoff on error capped at
// 30m, honouring retryAfter exactly on rate_limited, unhealthy after 5
// consecutive errors or a non-advancing cursor with items across 3 polls.
type Poller struct {
	cfg   PollerConfig
	store eventstore.Store
	log   *slog.Logger
}

func NewPoller(cfg PollerConfig, store eventstore.Store) *Poller {
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	return &Poller{cfg: cfg, store: store, log: log}
}

// Run polls until ctx is cancelled. It never returns an error — every
// failure is recorded as source health and logged, per AGENTS §4 rule 1
// (no silent failure, but also no crashing the daemon over one flaky
// integration).
func (p *Poller) Run(ctx context.Context) {
	consecutiveErrors := 0
	nonAdvancing := 0
	flushes := make(chan Flush, 8)
	vc := NewVolumeController(p.cfg.Volume, flushes)
	go p.drainFlushes(ctx, flushes)

	for {
		src, ok, err := p.store.GetSource(ctx, p.cfg.SourceID)
		if err != nil {
			p.log.Error("tasksource: reading source", "source", p.cfg.SourceID, "err", err)
			return
		}
		if !ok || !src.Enabled {
			return
		}

		cursor, etag, _, err := p.store.GetSourceCursor(ctx, p.cfg.SourceID)
		if err != nil {
			p.log.Error("tasksource: reading cursor", "source", p.cfg.SourceID, "err", err)
			return
		}

		out, err := p.cfg.Source.Poll(ctx, PollInput{Cursor: cursor, ETag: etag, Limit: 50})
		interval := p.cfg.Interval
		if interval <= 0 {
			interval = 120 * time.Second
		}

		if err != nil {
			consecutiveErrors++
			retryAfter, unhealthy := p.classifyError(err, consecutiveErrors)
			health := eventstore.SourceHealth{
				Status: "unhealthy", Reason: err.Error(), ConsecutiveErrors: consecutiveErrors,
			}
			if !unhealthy {
				health.Status = "throttled"
			}
			now := time.Now().UTC()
			next := now.Add(retryAfter)
			health.LastPollAt, health.NextPollAt = &now, &next
			_ = p.store.SetSourceHealth(ctx, p.cfg.SourceID, health)
			if unhealthy {
				p.log.Error("tasksource: source unhealthy, stopping", "source", p.cfg.SourceID, "err", err)
				return
			}
			if !p.sleep(ctx, retryAfter) {
				return
			}
			continue
		}

		if out.Cursor == cursor && len(out.Items) > 0 {
			nonAdvancing++
			if nonAdvancing >= 3 {
				_ = p.store.SetSourceHealth(ctx, p.cfg.SourceID, eventstore.SourceHealth{
					Status: "unhealthy", Reason: "cursor not advancing with items across 3 polls",
				})
				p.log.Error("tasksource: source unhealthy, non-advancing cursor", "source", p.cfg.SourceID)
				return
			}
		} else {
			nonAdvancing = 0
		}
		consecutiveErrors = 0

		if err := p.store.SetSourceCursor(ctx, p.cfg.SourceID, out.Cursor, out.ETag); err != nil {
			p.log.Error("tasksource: persisting cursor", "source", p.cfg.SourceID, "err", err)
		}
		now := time.Now().UTC()
		next := now.Add(interval)
		_ = p.store.SetSourceHealth(ctx, p.cfg.SourceID, eventstore.SourceHealth{
			Status: "healthy", LastPollAt: &now, NextPollAt: &next,
		})

		for _, item := range out.Items {
			if err := ValidateWorkItem(item); err != nil {
				p.log.Error("tasksource: invalid work item", "source", p.cfg.SourceID, "err", err)
				continue
			}
			vc.Add(item, len(out.Items))
		}

		if out.PollAfter > 0 {
			interval = out.PollAfter
		}
		jitter := time.Duration(rand.Int63n(int64(interval)/4 + 1))
		if !p.sleep(ctx, interval+jitter-interval/8) {
			return
		}
	}
}

func (p *Poller) drainFlushes(ctx context.Context, flushes <-chan Flush) {
	for {
		select {
		case <-ctx.Done():
			return
		case f := <-flushes:
			p.handleFlush(ctx, f)
		}
	}
}

func (p *Poller) handleFlush(ctx context.Context, f Flush) {
	if f.Digest {
		p.createDigestRun(ctx, f.Items)
		return
	}
	for _, item := range f.Items {
		p.createItemRun(ctx, item)
	}
}

func (p *Poller) createItemRun(ctx context.Context, item WorkItem) {
	flow := item.Flow
	if flow == "" {
		flow = p.cfg.DefaultFlow
	}
	runID, created, err := TriggerRun(ctx, p.store, item.DedupeKey, p.cfg.SourceID, item.ID,
		CreateRunRequest{
			DefinitionRef: flow, Params: item.Params,
			TriggerRef: fmt.Sprintf("poll:%s:%s", p.cfg.SourceID, item.ID),
			Actor:      "trigger:poll",
		}, p.cfg.Limits)
	if err != nil {
		p.reportRejection(ctx, item, err)
		return
	}
	if created {
		_, _ = Ack(ctx, p.store, p.cfg.Source, AckInput{
			ItemID: item.ID, DedupeKey: item.DedupeKey, Outcome: "triggered", RunID: runID,
			IdempotencyKey: "trigger:" + item.DedupeKey,
		})
	}
}

func (p *Poller) createDigestRun(ctx context.Context, items []WorkItem) {
	if len(items) == 0 {
		return
	}
	// A digest run's dedupe key spans the whole batch, not one item —
	// re-flushing the identical set of items is a no-op like any other
	// trigger.
	digestKey := "digest:" + p.cfg.SourceID + ":" + items[0].DedupeKey
	runID, created, err := TriggerRun(ctx, p.store, digestKey, p.cfg.SourceID, "digest",
		CreateRunRequest{
			DefinitionRef: p.cfg.DefaultFlow,
			TriggerRef:    fmt.Sprintf("poll:%s:digest", p.cfg.SourceID),
			Actor:         "trigger:poll",
		}, p.cfg.Limits)
	if err != nil {
		for _, item := range items {
			p.reportRejection(ctx, item, err)
		}
		return
	}
	if created {
		for _, item := range items {
			_, _ = Ack(ctx, p.store, p.cfg.Source, AckInput{
				ItemID: item.ID, DedupeKey: item.DedupeKey, Outcome: "triggered", RunID: runID,
				IdempotencyKey: "trigger:" + item.DedupeKey,
			})
		}
	}
}

func (p *Poller) reportRejection(ctx context.Context, item WorkItem, cause error) {
	outcome := "failed"
	if errors.Is(cause, ErrQueueFull) || errors.Is(cause, ErrTooManyOpenDecisions) {
		outcome = "rejected"
	}
	_, _ = Ack(ctx, p.store, p.cfg.Source, AckInput{
		ItemID: item.ID, DedupeKey: item.DedupeKey, Outcome: outcome, Reason: cause.Error(),
		IdempotencyKey: "reject:" + item.DedupeKey,
	})
	p.log.Warn("tasksource: run rejected", "source", p.cfg.SourceID, "item", item.ID, "err", cause)
}

// classifyError turns a Source error into a backoff duration and whether
// the source should be marked unhealthy and stop polling.
func (p *Poller) classifyError(err error, consecutiveErrors int) (retryAfter time.Duration, unhealthy bool) {
	var se *SourceError
	if errors.As(err, &se) {
		if se.Code == ErrRateLimited && se.RetryAfter > 0 {
			return se.RetryAfter, false
		}
	}
	if consecutiveErrors >= 5 {
		return 0, true
	}
	backoff := p.cfg.Interval
	if backoff <= 0 {
		backoff = 120 * time.Second
	}
	shift := consecutiveErrors
	if shift > 5 {
		shift = 5
	}
	backoff *= time.Duration(1 << shift)
	if backoff > 30*time.Minute {
		backoff = 30 * time.Minute
	}
	return backoff, false
}

func (p *Poller) sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		d = time.Millisecond
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
