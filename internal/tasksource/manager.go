package tasksource

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/williamokano/kairos/internal/eventstore"
)

// ManagerConfig assembles a Manager.
type ManagerConfig struct {
	InboxDir                    string
	InboxEnabled                bool
	DefaultFlow, DefaultProject string
	Limits                      QueueLimits
	Registry                    *Registry
	Log                         *slog.Logger
}

// Manager owns every trigger mechanism's goroutine: the inbox watcher,
// one poller per enabled poll-kind source, and one scheduler per enabled
// cron-kind source. cmd/kairos/serve.go constructs exactly one and calls
// Start once, after engine.Reconcile, matching every other subsystem's
// boot-order discipline in this daemon.
type Manager struct {
	cfg   ManagerConfig
	store eventstore.Store
	log   *slog.Logger

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func NewManager(cfg ManagerConfig, store eventstore.Store) *Manager {
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	if cfg.Registry == nil {
		cfg.Registry = NewRegistry()
	}
	return &Manager{cfg: cfg, store: store, log: log}
}

// Start launches every enabled source's goroutine and returns
// immediately; it never blocks. Call Stop to shut down.
func (m *Manager) Start(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel

	if m.cfg.InboxEnabled && m.cfg.InboxDir != "" {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			if err := RunInbox(runCtx, InboxConfig{
				Dir: m.cfg.InboxDir, DefaultFlow: m.cfg.DefaultFlow, DefaultProject: m.cfg.DefaultProject,
				Limits: m.cfg.Limits, Log: m.log,
			}, m.store); err != nil {
				m.log.Error("tasksource: inbox watcher exited", "err", err)
			}
		}()
	}

	sources, err := m.store.ListSources(ctx)
	if err != nil {
		return fmt.Errorf("listing sources: %w", err)
	}
	for _, src := range sources {
		if !src.Enabled {
			continue
		}
		switch src.Kind {
		case "cron":
			m.startCron(runCtx, src)
		case "inbox":
			// handled above, unconditionally at InboxDir — a `source`
			// row of kind "inbox" exists only for `kairos src ls`
			// visibility, not a second watcher.
		default:
			m.startPoller(runCtx, src)
		}
	}
	return nil
}

func (m *Manager) startPoller(ctx context.Context, src eventstore.Source) {
	s, err := m.cfg.Registry.Build(src.Kind, []byte(src.Config))
	if err != nil {
		m.log.Error("tasksource: building source", "source", src.ID, "kind", src.Kind, "err", err)
		return
	}
	p := NewPoller(PollerConfig{
		SourceID: src.ID, Source: s, Interval: time.Duration(src.IntervalSeconds) * time.Second,
		DefaultFlow: src.Flow, DefaultProject: src.Project, Limits: m.cfg.Limits, Log: m.log,
	}, m.store)
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		p.Run(ctx)
	}()
}

// CronSourceConfig is the source.config JSON shape a `kind: cron` row
// carries. Exported so the CLI's and the web UI's friendly-flag/form
// entry points for creating a cron source both build the IDENTICAL
// config a hand-written --config JSON string would have produced — one
// shape, two ergonomic front doors, never a divergent schema (see
// BuildCronConfig).
type CronSourceConfig struct {
	Schedule string `json:"schedule"` // "daily" | "weekly"
	Weekday  int    `json:"weekday,omitempty"`
	Hour     int    `json:"hour"`
	Minute   int    `json:"minute"`
}

type cronSourceConfig = CronSourceConfig

// BuildCronConfig validates schedule/weekday/hour/minute and renders the
// real source.config JSON string startCron itself parses — the single
// place this shape is constructed, shared by internal/cli's `kairos src
// add cron` and internal/web's cron-source form (08-triggers.md's own
// named Future work: "a friendlier per-kind flag surface... is cosmetic,
// deferred" — closed here for the one kind, cron, that actually has a
// real, constructible Source today; see L29-flow-and-source-authoring.md
// for why github/jira/linear/plugin kinds are NOT given the same
// treatment in this pass).
func BuildCronConfig(schedule string, weekday, hour, minute int) (string, error) {
	if schedule != "daily" && schedule != "weekly" {
		return "", fmt.Errorf(`tasksource: schedule must be "daily" or "weekly", got %q`, schedule)
	}
	if hour < 0 || hour > 23 {
		return "", fmt.Errorf("tasksource: hour must be 0-23, got %d", hour)
	}
	if minute < 0 || minute > 59 {
		return "", fmt.Errorf("tasksource: minute must be 0-59, got %d", minute)
	}
	if schedule == "weekly" && (weekday < 0 || weekday > 6) {
		return "", fmt.Errorf("tasksource: weekday must be 0-6 (Sunday-Saturday), got %d", weekday)
	}
	cfg := CronSourceConfig{Schedule: schedule, Hour: hour, Minute: minute}
	if schedule == "weekly" {
		cfg.Weekday = weekday
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("tasksource: marshalling cron config: %w", err)
	}
	return string(b), nil
}

func (m *Manager) startCron(ctx context.Context, src eventstore.Source) {
	var cfg cronSourceConfig
	if err := json.Unmarshal([]byte(src.Config), &cfg); err != nil {
		m.log.Error("tasksource: parsing cron config", "source", src.ID, "err", err)
		return
	}
	var sched Schedule
	switch cfg.Schedule {
	case "weekly":
		sched = Weekly{Weekday: time.Weekday(cfg.Weekday), Hour: cfg.Hour, Minute: cfg.Minute}
	default:
		sched = Daily{Hour: cfg.Hour, Minute: cfg.Minute}
	}

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.runCron(ctx, src, sched)
	}()
}

// runCron is 08-triggers.md's "catchUp: skip" made real: on every wake
// (boot or fire), it computes only the SINGLE next occurrence — never a
// backlog — and records a cold start (a gap exceeding 2x cadence) as
// `source.resumed{gap}`-equivalent health, per the doc's wall-clock-jump
// handling.
func (m *Manager) runCron(ctx context.Context, src eventstore.Source, sched Schedule) {
	for {
		// A cron source has no upstream cursor to track, so this reuses
		// source_cursor's etag column to hold "when did this last fire"
		// instead — cheaper than a fourth table for one timestamp.
		_, lastFiredStr, ok, err := m.store.GetSourceCursor(ctx, src.ID)
		var lastFired time.Time
		if ok && lastFiredStr != "" {
			lastFired, _ = time.Parse(time.RFC3339, lastFiredStr)
		}
		if err != nil {
			m.log.Error("tasksource: reading cron cursor", "source", src.ID, "err", err)
			return
		}

		now := time.Now().UTC()
		next, coldStart := CronCatchUp(sched, lastFired, now)
		if coldStart {
			m.log.Warn("tasksource: cron cold start, skipping missed occurrences", "source", src.ID, "gap", now.Sub(lastFired))
		}

		t := time.NewTimer(next.Sub(now))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}

		fired := time.Now().UTC()
		_ = m.store.SetSourceCursor(ctx, src.ID, "", fired.Format(time.RFC3339))
		if _, _, err := CreateRun(ctx, m.store, CreateRunRequest{
			DefinitionRef: src.Flow, TriggerRef: "cron:" + src.ID, Actor: "trigger:cron",
		}, m.cfg.Limits); err != nil {
			m.log.Error("tasksource: cron run creation failed", "source", src.ID, "err", err)
		}
	}
}

// Stop cancels every source's goroutine and waits for them to exit.
func (m *Manager) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
}
