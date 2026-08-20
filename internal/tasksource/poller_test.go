package tasksource_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/eventstore"
	"github.com/williamokano/kairos/internal/tasksource"
)

func registerSource(t *testing.T, st eventstore.Store, id string) {
	t.Helper()
	if err := st.UpsertSource(context.Background(), eventstore.Source{
		ID: id, Kind: "fake", Config: "{}", Enabled: true, IntervalSeconds: 1,
	}); err != nil {
		t.Fatalf("UpsertSource: %v", err)
	}
}

func TestPoller_itemProducesARunAndCursorPersists(t *testing.T) {
	st := openStore(t)
	registerSource(t, st, "src1")

	var calls int32
	src := &tasksource.Fake{
		PollFn: func(ctx context.Context, in tasksource.PollInput) (tasksource.PollOutput, error) {
			n := atomic.AddInt32(&calls, 1)
			if n == 1 {
				return tasksource.PollOutput{
					Items:  []tasksource.WorkItem{{ID: "1", DedupeKey: "poll-dedupe-1"}},
					Cursor: "cursor-1",
				}, nil
			}
			return tasksource.PollOutput{Cursor: in.Cursor}, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := tasksource.NewPoller(tasksource.PollerConfig{
		SourceID: "src1", Source: src, Interval: 50 * time.Millisecond, DefaultFlow: demoFlow(t),
	}, st)
	go p.Run(ctx)

	waitForRuns(t, st, 1, 3*time.Second)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		cursor, _, ok, err := st.GetSourceCursor(context.Background(), "src1")
		if err == nil && ok && cursor == "cursor-1" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("cursor never persisted as cursor-1")
}

func TestPoller_itemWithNoDedupeKeyIsSkippedNotPanicked(t *testing.T) {
	st := openStore(t)
	registerSource(t, st, "src2")

	src := &tasksource.Fake{
		PollFn: func(ctx context.Context, in tasksource.PollInput) (tasksource.PollOutput, error) {
			return tasksource.PollOutput{
				Items:  []tasksource.WorkItem{{ID: "no-dedupe"}}, // DedupeKey empty — a contract violation
				Cursor: "c",
			}, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := tasksource.NewPoller(tasksource.PollerConfig{
		SourceID: "src2", Source: src, Interval: 20 * time.Millisecond, DefaultFlow: demoFlow(t),
	}, st)
	go p.Run(ctx)

	time.Sleep(300 * time.Millisecond)
	runs, err := st.ListRuns(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("len(runs) = %d, want 0 (invalid item must never create a run)", len(runs))
	}
}

func TestPoller_rejectedTriggerIsAckedAsRejected(t *testing.T) {
	st := openStore(t)
	registerSource(t, st, "src3")
	// Fill the queue to 1 so the very next trigger is rejected.
	if _, _, err := tasksource.CreateRun(context.Background(), st, tasksource.CreateRunRequest{
		DefinitionRef: demoFlow(t), TriggerRef: "seed",
	}, tasksource.QueueLimits{}); err != nil {
		t.Fatalf("seeding a run: %v", err)
	}

	var polled int32
	src := &tasksource.Fake{
		PollFn: func(ctx context.Context, in tasksource.PollInput) (tasksource.PollOutput, error) {
			if atomic.AddInt32(&polled, 1) > 1 {
				return tasksource.PollOutput{Cursor: in.Cursor}, nil
			}
			return tasksource.PollOutput{
				Items:  []tasksource.WorkItem{{ID: "x", DedupeKey: "reject-me"}},
				Cursor: "c1",
			}, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := tasksource.NewPoller(tasksource.PollerConfig{
		SourceID: "src3", Source: src, Interval: 20 * time.Millisecond, DefaultFlow: demoFlow(t),
		Limits: tasksource.QueueLimits{MaxQueued: 1},
	}, st)
	go p.Run(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if src.AckCount() > 0 {
			ack := src.Acks[0]
			if ack.Outcome != "rejected" {
				t.Errorf("ack outcome = %q, want rejected", ack.Outcome)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("expected a rejected ack, never observed one")
}
