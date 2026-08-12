# 0004 — Admission control without placement

**Status:** Accepted · supersedes the ancestor design's scheduler
**Date:** 2026-08-12

## Context

The ancestor design had a Kubernetes-shaped scheduler making two decisions: **admission** ("may this work
start at all, given resources?") and **placement** ("which machine should it run on?"). Placement was
filter → score → bind, with roughly twelve predicates and eight scorers, plus workspace affinity, cache
affinity, pool warmth, spreading, packing, preemption, draining, and capacity-drift detection.

With one host there is nothing to place. But admission was never about machines — model slots, provider
rate limits, spend budgets, and human queue depth are all global or per-project — so it survives whole.

## Decision

Delete placement. Keep admission, and let it absorb the two placement predicates that were really about
*capacity* and the one that was really about *exclusivity*.

`internal/scheduler` does not exist. `internal/admission` answers one question per node execution — *may
it start right now?* — returning `Granted(claims)`, `Queued(position, estWait)`, or `Denied(reason)`.

**Survives:** pools of kind `concurrency` (node slots, `cpu.heavy`, model slots per class), `rate`
(provider RPM, GitHub API), `budget` (USD and tokens per window), `human` (open-task cap), and `port`.
All-or-nothing claims in canonical pool order, every claim leased, no overcommit. Priority classes with
aging. Explicit rejection past a queue depth rather than silent dropping.

**Dies:** filter/score/bind as phases, all eight scorers, machine labels and selectors, taints and
tolerations, affinity as a *score*, spreading, packing, power-aware placement, bursting, capacity vectors,
and the machine registry.

**Becomes a mutex:** the one-writer-per-workspace rule. The entire affinity machinery was approximating
exactly this, and locally it is a `flock`.

Two additions the ancestor design did not need because it had preemption: a **reserved `interactive` lane**
per pool, and a **per-run slot cap** (`maxSlotsPerRun`, default 1; 2 for coordinators).

## Consequences

**Good.** Roughly two thousand lines and twenty predicate/scorer tests become about four hundred lines and
one mutex. The reason a run is waiting becomes a single string the UI can print verbatim — `"2 of 2 claude
processes busy"`, `"$24.10 of $25.00 spent today"`, `"5 decisions already waiting on you"` — which is the
whole diagnostic surface on a system with no `kubectl describe`.

**Bad, and this is the real cost: preemption is foreclosed.** A ninety-minute implement node holds its slot
and aging cannot evict it, so the worst-case wait for a queued request is the longest running node's
remaining time, bounded only by node timeouts. The replacements are weaker but adequate at one user:
reserved interactive lanes mean a typed question is answered while four background runs proceed, and
per-run caps stop a twelve-child fan-out taking every model slot for two hours.

Also: a *release* always goes through the queue — there is no direct re-acquire — or a tight
run/gate/retry loop holds a slot indefinitely while an aged waiter watches and the queue looks healthy.

## Alternatives considered

- **Keep preemption** — it was already `enabled: false` in the ancestor design. The operator is sitting in
  front of the machine, and `kairos cancel` is manual preemption with better judgement than a scorer.
- **Keep a degenerate placement phase for "future-proofing"** — a filter loop over a set of size one is
  `if !ok { fail }`, and every domain concept admitted into a scheduler becomes permanent.
- **Drop admission too and just run everything** — twelve agents against three concurrent sessions gives
  rate-limit errors, retry storms, and a confusing bill. This is the failure the whole mechanism exists to
  prevent, and it is worse locally because the bill is personal.

## Revisit when

`kairos_admission_queue_wait_seconds{priority="interactive"}` p95 exceeds **60 seconds** over a week — that
means the reserved lane is not doing the job preemption used to, and the head-of-line problem has become
real rather than theoretical. Fix by shortening node timeouts or adding a lane before reopening preemption.
