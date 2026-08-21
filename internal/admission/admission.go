// Package admission answers one question per node execution — "may it
// start right now?" — returning Granted, Queued, or Denied, per ADR 0004
// and 02-config.md's ordered admission rules. It knows pools, capacities,
// and spend; it has never heard of a node's actor or a run's graph
// (01-architecture.md's L7: "admission is not domain-aware").
package admission

import (
	"fmt"
	"sync"
	"time"
)

// Outcome is the result of one admission decision.
type Outcome int

const (
	// Granted means every claim the request needed was leased; the caller
	// may start the node execution now and must call Release(claims) once
	// it finishes.
	Granted Outcome = iota
	// Queued means capacity is busy but the request may wait — the caller
	// is responsible for holding it and retrying once a Release happens
	// (Manager itself holds no wait queue; see Request.QueueDepth).
	Queued
	// Denied means the request must not run and must not wait — either a
	// hard capacity/budget/policy rule, or the queue is already at
	// maxQueued (02-config.md rule 7: reject, not queue, past that
	// depth — silent truncation is not acceptable here).
	Denied
)

// Claims is an opaque token identifying the pool leases a Granted decision
// created. Pass it back to Release exactly once, when the work finishes.
type Claims struct {
	held []claimID
}

type claimID struct {
	pool string
	key  string
}

// Decision is the result of one TryAdmit call.
type Decision struct {
	Outcome Outcome
	Claims  Claims
	// Reason is the human-readable, verbatim-printable string 02-config.md
	// specifies (e.g. "4 of 4 slots busy") — the whole diagnostic surface
	// on a system with no `kubectl describe`.
	Reason string
	// Position is this request's place in the queue, 1-indexed, set only
	// when Outcome == Queued.
	Position int
}

// Request is one node execution's admission ask. All claims are
// all-or-nothing: a request needing two pool slots where only one is
// available gets neither.
type Request struct {
	RunID  string
	NodeID string

	// NodeSlot is true for every node execution (rule 2: the "nodes"
	// concurrency pool). There is no case where a node execution consumes
	// zero concurrency, so callers always set this.
	NodeSlot bool

	// WorkspaceKey, if non-empty, claims the one-writer-per-workspace
	// mutex (rule 3) for this key — the engine's configured WorkspaceRepo
	// string, since only one daemon-wide repo exists as of L06/L08 (see
	// L07-admission.md's documented decisions).
	WorkspaceKey string

	// ModelClass, if non-empty, claims one slot from that class's model
	// pool (rule 4).
	ModelClass string

	// EstimatedCostUSD, if > 0, is added to the daily spend estimate
	// (rule 5) and checked against DailyUSD. This is an ESTIMATE, not
	// metered actual spend — NL-30 registers that Kairos cannot observe
	// real LLM cost yet, so rule 5 enforces against configured/declared
	// estimates only.
	EstimatedCostUSD float64

	// QueueDepth is the caller's current count of already-queued requests
	// (the engine owns the actual wait queue — Manager holds no queue
	// state of its own). Used only to evaluate rule 7 (maxQueued).
	QueueDepth int
}

// Config sizes every pool. Zero values fall back to 02-config.md's
// defaults table.
type Config struct {
	// NodeSlots is admission.nodes — concurrent node executions.
	NodeSlots int
	// ModelSlots maps a model class name to its concurrent-slot capacity
	// (admission.models.<class>.slots).
	ModelSlots map[string]int
	// DailyUSD is limits.dailyUSD — see Request.EstimatedCostUSD's caveat.
	DailyUSD float64
	// MaxQueued is admission.maxQueued — rule 7's reject threshold.
	MaxQueued int

	// Clock returns the current time, used only to compute rule 5's day
	// key (local time — 02-config.md frames dailyUSD as "your card", a
	// single-user tool with no timezone to reconcile across). Defaults to
	// time.Now if nil; tests inject a fixed/steppable clock instead of
	// sleeping across a real midnight.
	Clock func() time.Time
	// OnSpendChange, if set, is called synchronously after every change to
	// the running day total — both an admitted request's increment and a
	// day rollover's reset to zero — so a caller can persist it (see
	// internal/eventstore's GetAdmissionSpend/SetAdmissionSpend). Manager
	// itself performs no I/O; this is the seam that makes the reset
	// durable without admission importing a store.
	OnSpendChange func(day string, spentUSD float64)
}

// Manager holds every pool's live state. Safe for concurrent use.
type Manager struct {
	mu sync.Mutex

	clock         func() time.Time
	onSpendChange func(day string, spentUSD float64)

	draining bool

	nodeSlots    int
	nodeHeld     int
	modelSlots   map[string]int
	modelHeld    map[string]int
	workspaceKey map[string]bool // key -> held

	dailyUSD   float64
	day        string // "2026-08-21" — the day dailySpent is counted against
	dailySpent float64

	maxQueued int
}

// New constructs a Manager. NodeSlots/DailyUSD/MaxQueued of zero disable
// that rule's capacity check entirely (unlimited) — 02-config.md's
// defaults are resolved by the caller (cmd/kairos/serve.go), not here, so
// a zero-value Config is a deliberate "no limit" rather than "always
// deny". Call Seed after New, before the first TryAdmit, to restore a
// persisted running total from a prior boot on the same day.
func New(cfg Config) *Manager {
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	m := &Manager{
		nodeSlots:     cfg.NodeSlots,
		modelSlots:    cfg.ModelSlots,
		modelHeld:     make(map[string]int),
		workspaceKey:  make(map[string]bool),
		dailyUSD:      cfg.DailyUSD,
		maxQueued:     cfg.MaxQueued,
		clock:         clock,
		onSpendChange: cfg.OnSpendChange,
	}
	m.day = dayKey(clock())
	return m
}

// dayKey is rule 5's day boundary: the local calendar date, formatted so
// it sorts and compares as a plain string.
func dayKey(t time.Time) string {
	return t.Local().Format("2006-01-02")
}

// Today reports the day key Manager is currently counting spend against —
// what a caller must pass to Seed for it to take effect.
func (m *Manager) Today() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.day
}

// Seed restores a persisted running total for day — a no-op if day is not
// today (a restart that crosses midnight correctly starts today at zero,
// the same as a fresh boot; there is no "yesterday's leftover budget" to
// carry forward). Call once at construction, before the first TryAdmit.
func (m *Manager) Seed(day string, spentUSD float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if day != m.day {
		return
	}
	m.dailySpent = spentUSD
}

// SetDraining flips rule 1: once draining, every TryAdmit is Denied
// ("shutting down"), regardless of pool capacity. The engine calls this
// from Stop before interrupting in-flight nodes.
func (m *Manager) SetDraining(draining bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.draining = draining
}

// TryAdmit evaluates rules 1 through 5 and 7, in that order (first failure
// wins, matching 02-config.md's list exactly), against req. Rules 6
// (maxOpenDecisions), 8 (runner labels), and 9 (domain lanes) are not
// evaluated — see L07-admission.md's documented decisions for why each is
// deferred rather than silently guessed at.
func (m *Manager) TryAdmit(req Request) Decision {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.rolloverDayIfNeeded()

	// Rule 1: draining.
	if m.draining {
		return Decision{Outcome: Denied, Reason: "shutting down"}
	}

	// Rule 2: node concurrency.
	if req.NodeSlot && m.nodeSlots > 0 && m.nodeHeld >= m.nodeSlots {
		return m.queueOrDeny(req, fmt.Sprintf("%d of %d slots busy", m.nodeHeld, m.nodeSlots))
	}

	// Rule 3: one writer per workspace.
	if req.WorkspaceKey != "" && m.workspaceKey[req.WorkspaceKey] {
		return m.queueOrDeny(req, fmt.Sprintf("%s: another run is writing", req.WorkspaceKey))
	}

	// Rule 4: model slots.
	if req.ModelClass != "" {
		slots := m.modelSlots[req.ModelClass]
		if slots > 0 && m.modelHeld[req.ModelClass] >= slots {
			return m.queueOrDeny(req, fmt.Sprintf("%d of %d %s processes busy", m.modelHeld[req.ModelClass], slots, req.ModelClass))
		}
	}

	// Rule 5: daily spend cap (estimate-based — see EstimatedCostUSD's doc).
	if m.dailyUSD > 0 && m.dailySpent+req.EstimatedCostUSD > m.dailyUSD {
		return Decision{Outcome: Denied, Reason: fmt.Sprintf("$%.2f of $%.2f spent today", m.dailySpent, m.dailyUSD)}
	}

	// Every check passed: grant every claim this request needs,
	// all-or-nothing (nothing above returned early once we reach here, so
	// leasing below can never partially fail).
	var claims Claims
	if req.NodeSlot {
		m.nodeHeld++
		claims.held = append(claims.held, claimID{pool: "nodes", key: ""})
	}
	if req.WorkspaceKey != "" {
		m.workspaceKey[req.WorkspaceKey] = true
		claims.held = append(claims.held, claimID{pool: "workspace", key: req.WorkspaceKey})
	}
	if req.ModelClass != "" {
		m.modelHeld[req.ModelClass]++
		claims.held = append(claims.held, claimID{pool: "model", key: req.ModelClass})
	}
	if req.EstimatedCostUSD > 0 {
		m.dailySpent += req.EstimatedCostUSD
		claims.held = append(claims.held, claimID{pool: "budget", key: fmt.Sprintf("%f", req.EstimatedCostUSD)})
		m.notifySpendChange()
	}
	return Decision{Outcome: Granted, Claims: claims}
}

// rolloverDayIfNeeded resets dailySpent to zero the first time TryAdmit
// observes a new calendar day — the fix for the counter being
// process-lifetime-only: without Seed restoring today's total at boot,
// AND without this check, a daemon that stays up across midnight would
// never reset at all, permanently denying once yesterday's estimate
// crossed dailyUSD.
func (m *Manager) rolloverDayIfNeeded() {
	today := dayKey(m.clock())
	if today == m.day {
		return
	}
	m.day = today
	m.dailySpent = 0
	m.notifySpendChange()
}

func (m *Manager) notifySpendChange() {
	if m.onSpendChange != nil {
		m.onSpendChange(m.day, m.dailySpent)
	}
}

// queueOrDeny implements rule 7: past maxQueued, a busy pool produces a
// hard Denied rather than a Queued — 02-config.md is explicit that silent
// truncation past this point "lies", so a misconfigured integration
// firing 500 events must produce 500 visible rejections once the queue is
// full, not an ever-growing queue.
func (m *Manager) queueOrDeny(req Request, busyReason string) Decision {
	if m.maxQueued > 0 && req.QueueDepth >= m.maxQueued {
		return Decision{Outcome: Denied, Reason: fmt.Sprintf("queue full (%d of %d)", req.QueueDepth, m.maxQueued)}
	}
	return Decision{Outcome: Queued, Reason: busyReason, Position: req.QueueDepth + 1}
}

// Release frees every pool slot a Granted Decision's Claims leased. Safe
// to call at most once per Decision; calling it on a zero-value Claims
// (e.g. from a Queued or Denied decision) is a no-op.
func (m *Manager) Release(claims Claims) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range claims.held {
		switch c.pool {
		case "nodes":
			if m.nodeHeld > 0 {
				m.nodeHeld--
			}
		case "workspace":
			delete(m.workspaceKey, c.key)
		case "model":
			if m.modelHeld[c.key] > 0 {
				m.modelHeld[c.key]--
			}
		case "budget":
			// Budget claims are never released — a day's spend does not
			// come back once incurred. (Daily-window reset is Future
			// work; see L07-admission.md.)
		}
	}
}
