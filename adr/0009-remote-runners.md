# 0009 — Remote runners are an executor implementation, not a fleet

**Status:** **Proposed** — a specification is being drafted in `07-runners.md`; not accepted
**Date:** 2026-08-12

## Context

`01-architecture.md` (L13′) and `11-limitations.md` both state that the multi-machine path is
**foreclosed, not deferred**. That statement needs to be read precisely, because it is about to be
partially amended and the corpus must not quietly contradict itself.

What was foreclosed is the **fleet**: placement, scoring, workspace affinity as a ranked signal, spreading
and packing, preemption, capacity planning, machine registration and heartbeats, workspace relocation, and
isolation as a guarantee. Re-adding *that* is a rewrite of admission, workspace ownership, and the event
bus.

What is cheap is **one more executor implementation plus per-run pinning** — precisely because
[0003](0003-one-execution-chokepoint.md) deleted the provider abstraction while deliberately preserving the
properties that make a second backend possible: `Spec` is pure data, process identity lives in events,
`Chunk` is already a wire format, and `Terminate(ctx, reason)` is the only cancellation API.

The user's framing was explicit: *"maybe, and only maybe… not the main focus, but able to plug new runners
just to scale a little bit."*

## Decision (proposed)

Add a `Runner` implementation alongside `local` — `ssh`, and/or the same binary in a runner mode — and a
`runsOn:` label on nodes. **A run is pinned to one runner for its entire life.**

**In scope:** the second implementation; per-run pinning declared at admission rather than scored; a
per-runner toolchain probe and health state; a per-runner reaper; label-match selection with `local` as the
default; and a `kairos runner` verb group.

**Out of scope, and staying out:** placement, scoring, affinity as a ranked signal, autoscaling, migration
of a live run, workspace replication, bin-packing, and any isolation claim whatsoever. Model slots,
provider rate limits, windows, and budgets stay **global** — one subscription is one subscription no matter
how many machines poll it. Concurrency, `cpu.heavy`, disk, and the toolchain are **per-runner**.

## Consequences

**Good.** A second machine you already own absorbs long build-and-test nodes without touching the engine,
the workflow spec, or the event model. Pinning is trivially satisfiable, so none of the placement machinery
returns.

**Bad.** A run pinned to a runner that becomes unreachable **cannot be migrated** — its workspace is over
there — so it parks rather than relocating. Artifacts and logs cross a network, so streaming needs
backpressure that a pipe did not. And the agent on the remote machine has *that* machine's credentials and
filesystem, so the blast radius widens rather than narrows.

## The open question this turns on

Gates are the mechanism that makes the whole system trustworthy, and the guarantee is that **the engine, not
the agent, runs them**. Locally the engine reads the tree directly. Remotely it cannot: `command`,
`coverage`, `file`, `regex`, and `git-diff` gates must execute *on the runner*, which means **the runner is
trusted to report its own exit code.**

That weakens the guarantee from *"the engine ran the check"* to *"the engine ran the check, or a runner the
operator trusts did"*. Whether that trade is acceptable — and what mitigates it (engine-chosen argv, the
runner speaking a protocol the agent cannot, typed streamed results, and the fact that only `expr` gates
remain engine-side) — is the question `07-runners.md` must answer before this ADR can move to `Accepted`.

## Alternatives considered

- **Do nothing; stay single-host** — the honest default, and still the right answer if the gate-trust
  question cannot be answered well.
- **Resurrect the full runtime abstraction and scheduler** — the thing that was deleted, at the cost that
  got it deleted.
- **Docker or Kubernetes as the second backend** — reintroduces images, registries, and a daemon dependency
  for a user who asked for "as if running on a remote machine", and it would imply isolation this design
  does not provide.
- **Run gates only for `expr` remotely and ship the tree back for the rest** — pays a full transfer per
  gate, and would make a lint gate slower than the node it gates.

## Revisit when

`07-runners.md` is reviewed. Move to `Accepted` only once the gate-trust consequence is written into
`11-limitations.md` as a registered limitation with a Detection line, and the sentences in
`01-architecture.md` and `11-limitations.md` about the foreclosed multi-machine path are amended to
distinguish *fleet* from *second executor*. If the answer is no, mark this `Rejected` and say why — an
un-decided ADR left sitting in `Proposed` is how a corpus starts lying about what it has decided.
