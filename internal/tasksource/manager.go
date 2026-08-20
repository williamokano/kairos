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

// cronSourceConfig is the source.config JSON shape a `kind: cron` row
// carries.
type cronSourceConfig struct {
	Schedule string `json:"schedule"` // "daily" | "weekly"
	Weekday  int    `json:"weekday,omitempty"`
	Hour     int    `json:"hour"`
	Minute   int    `json:"minute"`
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
