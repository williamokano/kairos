# 0008 — The terminal is a client, not a harness

**Status:** Accepted · the rule is inherited; the justification is replaced
**Date:** 2026-08-12

## Context

The ancestor design forbade the TUI from executing anything and justified it with a **threat model**: the
terminal runs on your laptop with your credentials, the laptop is not a registered Machine, and execution
belongs behind a kernel boundary in the execution plane.

In Kairos that justification is gone by construction. There is no kernel boundary, the host has all the
tooling, the credentials are already here, and the daemon and the TUI are the same binary in one process
tree. If the rule is defended with the old argument it will lose the argument — and then someone will put
agent execution in the render loop, correctly observing that it works.

The rule is still right. It needs new reasons.

## Decision

`internal/tui` and `internal/cli/chat` import neither `os/exec` nor `internal/executor/*`, and import the
API client rather than the engine. Enforced by `TestArchitecture_tuiHasNoExecution`.

The reasons that actually hold it up now, in order of weight:

1. **Durability, which is the whole product.** "Close the terminal and the work keeps going" is only true if
   the engine owns execution. A TUI that runs agents loses them to `Ctrl-C`, a `SIGWINCH` bug, an SSH drop,
   or a closed lid. Every replay, fork, and resume guarantee depends on the event log being the authority,
   and it cannot be if execution state lives in a Bubble Tea model.
2. **Two clients cannot diverge if neither executes.** The web UI is co-equal
   ([0007](0007-go-templates-and-htmx.md)); that is free when both read projections and impossible when one
   of them *is* the state.
3. **Headless is free.** `kairos do "…"` in a script, a git hook, or cron works only if the engine needs no
   TTY.
4. **Replay must not depend on terminal state.** Any execution-affecting state held by a renderer is
   unreplayable, silently, and you find out six weeks later when a fork diverges.

Restated as the rule to quote: **the client is not the executor — not because your host is untrusted, but
because a renderer's lifetime is a terminal session and work must outlive it.**

## Consequences

**Good.** `Ctrl-C` detaches instead of killing, which is the behaviour that makes a long autonomous run
feel safe to walk away from. Every capability is reachable from the API, so the CLI, the TUI, and the
browser stay honest about each other. The architecture test is cheap — an import-graph assertion, no threat
modelling required.

**Bad, and it must be said out loud.** In the ancestor design this boundary was *also* a network boundary
and a kernel boundary; here **an import-graph test is the only thing left holding it.** The first person
who wants a "just for benchmarking" or "offline mode" shortcut will be right that it would work, and the
test's failure message must therefore carry the durability argument rather than just the rule.

There is also a small real cost: the TUI pays a loopback round-trip for state it could have read from
memory. On a unix socket that is microseconds, and it buys the property outright.

## Alternatives considered

- **TUI owns the engine, daemon optional** — the simplest thing, and it deletes durability. Closing a
  terminal kills a three-hour run.
- **Allow execution in the TUI "only for benchmark mode"** — benchmark runs are exactly the ones you leave
  unattended, and an exception that exists is an exception that spreads.
- **Enforce it with a process boundary instead of a test** — that is the ancestor design, and
  [0002](0002-one-process-one-host.md) deleted it for good reasons. The test is the cheap replacement, and
  cheap is why it will actually be kept.

## Revisit when

Someone proposes an in-TUI execution path — for benchmarking, an offline mode, or "just this one shell
command". The answer is a new ADR that supersedes this one, not an exception inside it. Treat a request to
weaken `TestArchitecture_tuiHasNoExecution` as a request to delete the boundary, because that is what it is.
