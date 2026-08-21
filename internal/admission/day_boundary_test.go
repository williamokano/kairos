package admission_test

import (
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/admission"
)

// TestTryAdmit_spendSurvivesARestartOnTheSameDay is the enforcing test
// for the daily-spend-window fix: a Manager rebuilt with the SAME day
// key and a Seed call (the shape a real restart takes — Reconcile reads
// a persisted total and calls Seed before the first TryAdmit) resumes
// counting against the persisted total instead of starting over at
// zero. Before this fix, dailySpent had no Seed/Clock/OnSpendChange
// hooks at all and was always process-lifetime-only.
func TestTryAdmit_spendSurvivesARestartOnTheSameDay(t *testing.T) {
	fixedNow := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return fixedNow }

	var persisted struct {
		day   string
		spent float64
	}

	m1 := admission.New(admission.Config{
		DailyUSD: 25, Clock: clock,
		OnSpendChange: func(day string, spentUSD float64) { persisted.day, persisted.spent = day, spentUSD },
	})
	if d := m1.TryAdmit(admission.Request{EstimatedCostUSD: 20}); d.Outcome != admission.Granted {
		t.Fatalf("first request: got %+v, want Granted", d)
	}
	if persisted.spent != 20 {
		t.Fatalf("OnSpendChange recorded %.2f, want 20", persisted.spent)
	}

	// Simulate a daemon restart: a fresh Manager, same clock/day, seeded
	// from what the "prior boot" persisted.
	m2 := admission.New(admission.Config{DailyUSD: 25, Clock: clock})
	m2.Seed(persisted.day, persisted.spent)

	// $20 already spent today + a further $10 estimate exceeds the $25
	// cap — this only fails correctly if Seed's restored total is really
	// being enforced, not a fresh, silently-reset zero.
	d := m2.TryAdmit(admission.Request{EstimatedCostUSD: 10})
	if d.Outcome != admission.Denied {
		t.Fatalf("post-restart request: got %+v, want Denied (restart must not silently reset the cap)", d)
	}
	if d.Reason != "$20.00 of $25.00 spent today" {
		t.Fatalf("reason = %q, want the restored total reflected in it", d.Reason)
	}
}

// TestTryAdmit_genuineDayRolloverResetsSpend confirms the OTHER half of
// the fix: a Manager that stays alive across a real midnight (no
// restart at all) must still reset — otherwise a long-running daemon
// would permanently deny once any single day's estimate crossed
// dailyUSD, never seeing tomorrow's fresh budget.
func TestTryAdmit_genuineDayRolloverResetsSpend(t *testing.T) {
	// Two days apart at midday UTC — unambiguously different local
	// calendar dates under any real timezone offset, so this doesn't rely
	// on the test machine's local time being close to UTC.
	day1 := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	now := day1

	var resetSeen bool
	m := admission.New(admission.Config{
		DailyUSD: 25,
		Clock:    func() time.Time { return now },
		OnSpendChange: func(day string, spentUSD float64) {
			if spentUSD == 0 {
				resetSeen = true
			}
		},
	})

	if d := m.TryAdmit(admission.Request{EstimatedCostUSD: 24}); d.Outcome != admission.Granted {
		t.Fatalf("day1 request: got %+v, want Granted", d)
	}
	// Would deny under day1's total ($24 + $5 > $25) if the day never rolls over.
	now = day2
	d := m.TryAdmit(admission.Request{EstimatedCostUSD: 5})
	if d.Outcome != admission.Granted {
		t.Fatalf("day2 request: got %+v, want Granted (the day boundary must have reset spend to 0)", d)
	}
	if !resetSeen {
		t.Fatal("OnSpendChange was never called with spentUSD=0 — the rollover reset was not observed")
	}
	if got := m.Today(); got != day2.Local().Format("2006-01-02") {
		t.Fatalf("Today() = %q, want %s", got, day2.Local().Format("2006-01-02"))
	}
}
