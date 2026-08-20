// Package tasksource is 08-triggers.md's "one code path out": every way
// work can arrive without a human typing `kairos run` — the inbox,
// pollers, cron, webhooks, and third-party TaskSource plugins — resolves
// into exactly one call, CreateRun, which is the same call
// internal/api's POST /runs handler makes. There is no ad-hoc mode that
// bypasses the log.
//
// State a source needs to survive a restart (cursors, health, dedupe) is
// owned entirely by the daemon via internal/eventstore's trigger tables —
// never by a plugin, which cannot be restarted safely if it keeps its own
// cursor.
package tasksource
