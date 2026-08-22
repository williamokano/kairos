package tasksource_test

import (
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/tasksource"
)

func TestDaily_nextFiresTomorrowIfTodaysTimeAlreadyPassed(t *testing.T) {
	sched := tasksource.Daily{Hour: 3, Minute: 0}
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC) // past 03:00 already
	next := sched.Next(now)
	want := time.Date(2026, 8, 21, 3, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("Next = %v, want %v", next, want)
	}
}

func TestDaily_nextFiresLaterTodayIfNotYetPassed(t *testing.T) {
	sched := tasksource.Daily{Hour: 22, Minute: 0}
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	next := sched.Next(now)
	want := time.Date(2026, 8, 20, 22, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("Next = %v, want %v", next, want)
	}
}

func TestWeekly_nextFiresOnTheRightWeekday(t *testing.T) {
	sched := tasksource.Weekly{Weekday: time.Friday, Hour: 9, Minute: 0}
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC) // a Thursday
	next := sched.Next(now)
	if next.Weekday() != time.Friday {
		t.Errorf("Next().Weekday() = %v, want Friday", next.Weekday())
	}
	if next.Sub(now) > 25*time.Hour {
		t.Errorf("Next = %v is more than a day away from a Thursday->Friday schedule", next)
	}
}

func TestCronCatchUp_noColdStartOnFirstFire(t *testing.T) {
	sched := tasksource.Daily{Hour: 3, Minute: 0}
	_, coldStart := tasksource.CronCatchUp(sched, time.Time{}, time.Now())
	if coldStart {
		t.Error("expected no cold start when lastFired is zero (never fired before)")
	}
}

func TestCronCatchUp_coldStartAfterALongGap(t *testing.T) {
	sched := tasksource.Daily{Hour: 3, Minute: 0}
	lastFired := time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC)
	now := time.Date(2026, 8, 20, 3, 0, 0, 0, time.UTC) // 19 days later — a laptop closed for a long time
	next, coldStart := tasksource.CronCatchUp(sched, lastFired, now)
	if !coldStart {
		t.Error("expected a cold start after a 19-day gap on a daily schedule")
	}
	// The critical "catchUp: skip" property: next is the SINGLE next
	// occurrence, never a backlog of 19 missed days.
	if next.Sub(now) > 25*time.Hour {
		t.Errorf("next = %v, more than one day after now — looks like a backlog, not catchUp:skip", next)
	}
}

func TestCronCatchUp_noColdStartOnNormalCadence(t *testing.T) {
	sched := tasksource.Daily{Hour: 3, Minute: 0}
	lastFired := time.Date(2026, 8, 19, 3, 0, 0, 0, time.UTC)
	now := time.Date(2026, 8, 20, 3, 0, 1, 0, time.UTC) // one normal day later
	_, coldStart := tasksource.CronCatchUp(sched, lastFired, now)
	if coldStart {
		t.Error("expected no cold start on a normal, uninterrupted daily cadence")
	}
}

// TestBuildCronConfig_validInputsProduceExpectedJSON proves the CLI's
// `kairos src add cron` friendly flags and the web Sources form's cron
// fields build the IDENTICAL config shape startCron itself parses — one
// constructor, two ergonomic front doors, never a divergent schema.
func TestBuildCronConfig_validInputsProduceExpectedJSON(t *testing.T) {
	got, err := tasksource.BuildCronConfig("weekly", 2, 9, 30)
	if err != nil {
		t.Fatalf("BuildCronConfig: %v", err)
	}
	want := `{"schedule":"weekly","weekday":2,"hour":9,"minute":30}`
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestBuildCronConfig_dailyOmitsWeekdayEvenIfPassed(t *testing.T) {
	got, err := tasksource.BuildCronConfig("daily", 5, 9, 30)
	if err != nil {
		t.Fatalf("BuildCronConfig: %v", err)
	}
	// weekday is only meaningful for "weekly" — daily must not carry a
	// stray weekday value into the config a real cron source parses.
	if want := `{"schedule":"daily","hour":9,"minute":30}`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestBuildCronConfig_rejectsInvalidInputs(t *testing.T) {
	cases := []struct {
		name                  string
		schedule              string
		weekday, hour, minute int
	}{
		{"bad schedule", "monthly", 0, 9, 0},
		{"hour too high", "daily", 0, 24, 0},
		{"hour negative", "daily", 0, -1, 0},
		{"minute too high", "daily", 0, 9, 60},
		{"weekday too high for weekly", "weekly", 7, 9, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := tasksource.BuildCronConfig(c.schedule, c.weekday, c.hour, c.minute); err == nil {
				t.Errorf("expected an error for %s", c.name)
			}
		})
	}
}
