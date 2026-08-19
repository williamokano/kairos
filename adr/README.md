# Architecture Decision Records

Every decision that could reasonably have gone the other way, with its alternatives and the trigger that
would make us revisit it. The design documents argue these decisions in context; these records exist so
that "why is it this way" is answerable in two minutes rather than by reading four hundred lines.

## Conventions

- **Status** is one of `Proposed`, `Accepted`, `Rejected`, or `Superseded by NNNN`.
- **Immutable once `Accepted`.** A decision that changes gets a *new* ADR that supersedes the old one;
  the old one's status line is updated to point at its successor and nothing else about it is edited.
  Never edit an accepted ADR into a lie — the record is a history, not a description of the present.
- **Every ADR names a concrete revisit trigger.** Not "if this becomes a problem" but a number, a
  benchmark, a metric, or an event you could observe. The section people skip is the section that keeps
  the corpus from calcifying.
- Consequences are recorded honestly, including the bad ones. An ADR with no downsides listed has not
  been thought about.

Several of these supersede decisions made by the larger, distributed design this project was reduced
from. Where that happens the ADR says so and answers that design's stated objections directly, because
the objections were good and deserve replies rather than silence.

## Index

| | Decision | Status |
| --- | --- | --- |
| [0001](0001-sqlite-as-the-only-datastore.md) | SQLite as the only datastore | Accepted |
| [0002](0002-one-process-one-host.md) | One process, one host | Accepted |
| [0003](0003-one-execution-chokepoint.md) | Execution has exactly one chokepoint | Accepted |
| [0004](0004-admission-without-placement.md) | Admission control without placement | Accepted |
| [0005](0005-reference-clone-per-run.md) | A `--reference` clone per run | Accepted |
| [0006](0006-snapshots-are-git-refs.md) | Workspace snapshots are git refs plus optional CoW trees | Accepted |
| [0007](0007-go-templates-and-htmx.md) | Go templates plus htmx, not React | Accepted |
| [0008](0008-terminal-is-a-client.md) | The terminal is a client, not a harness | Accepted |
| [0009](0009-remote-runners.md) | Remote runners are an executor implementation, not a fleet | **Proposed** |
| [0010](0010-co-equal-surfaces-sse-plus-post.md) | Two co-equal surfaces, and realtime is SSE plus POST | Accepted |
| [0011](0011-polling-first-connectors.md) | Outbound polling is the default; webhooks are opt-in and BYO-tunnel | Accepted |
| [0012](0012-daemon-lock-without-flock.md) | The daemon lock is a PID file, not `flock` | **Proposed** |

**0009 and 0012 are not `Accepted`.** 0009's promotion condition is written into it: the gate-trust
consequence — a remote runner reports its own gate exit codes — must be registered as a limitation with a
Detection line before it can be accepted. That is now done (NL-14 in
[`../11-limitations.md`](../11-limitations.md)), so what remains is a decision to build it rather than a
gap in the reasoning. 0012's revisit trigger is L06 landing — `internal/executor/local` existing is what
lets the daemon lock move off a PID file onto a real kernel lock.
