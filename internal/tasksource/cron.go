package tasksource

import "time"

// Schedule computes the next fire time after a given instant.
// Documented decision (L16-triggers.md): this package implements Daily/
// Weekly schedules directly rather than a cron(5)-expression parser —
// 08-triggers.md's own examples ("Nightly dependency updates, weekly
// flake sweeps") are both covered exactly, and AGENTS.md's approved-
// dependency table names no cron library; adding one is an ADR this
// document does not need to spend. A real crontab-syntax Schedule is
// named in Future work.
type Schedule interface {
	Next(after time.Time) time.Time
}

// Daily fires once per day at Hour:Minute UTC.
type Daily struct{ Hour, Minute int }

func (d Daily) Next(after time.Time) time.Time {
	after = after.UTC()
	next := time.Date(after.Year(), after.Month(), after.Day(), d.Hour, d.Minute, 0, 0, time.UTC)
	if !next.After(after) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

// Weekly fires once a week on Weekday at Hour:Minute UTC.
type Weekly struct {
	Weekday      time.Weekday
	Hour, Minute int
}

func (w Weekly) Next(after time.Time) time.Time {
	after = after.UTC()
	next := time.Date(after.Year(), after.Month(), after.Day(), w.Hour, w.Minute, 0, 0, time.UTC)
	for next.Weekday() != w.Weekday || !next.After(after) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

// CronCatchUp is 08-triggers.md's "cron defaults to catchUp: skip, and
// that default must never change": given the last time this schedule
// actually fired (zero if never), the current wall clock, and the
// schedule itself, NextFire computes only the single next occurrence —
// never a backlog of every occurrence missed while the daemon was down.
// A laptop closed for fourteen hours must not wake to six nightly runs
// firing at once.
//
// coldStart reports whether the gap since lastFired exceeds twice the
// schedule's own cadence estimate (approximated here as the distance
// between two consecutive Next() calls) — the same wall-clock-jump
// detection the poller uses, so a restart after a long sleep is treated
// uniformly across both mechanisms.
func CronCatchUp(sched Schedule, lastFired, now time.Time) (nextFire time.Time, coldStart bool) {
	next1 := sched.Next(now)
	next2 := sched.Next(next1)
	cadence := next2.Sub(next1)
	if cadence <= 0 {
		cadence = 24 * time.Hour
	}
	if !lastFired.IsZero() && now.Sub(lastFired) > 2*cadence {
		coldStart = true
	}
	return next1, coldStart
}
