# ADR 0010 — Two co-equal surfaces, and realtime is SSE plus POST

**Status:** Accepted
**Date:** 2026-08-12
**Supersedes:** the "TUI-first, the browser is an inspector" position held in earlier drafts of
`09-cli-and-tui.md` and `10-webui.md`.
**Related:** [0002](0002-one-process-one-host.md) · [0007](0007-go-templates-and-htmx.md) ·
[0008](0008-terminal-is-a-client.md)

## Context

Two questions, and the first forces the second.

**Parity.** Earlier drafts split the surfaces by *job*: the terminal did the work, the browser read wide
evidence, and the dividing test told you to push anything frequent back into the TUI. The reasoning was
sound for a strict reduction — one person, one origin, don't build two of anything — but it produces a
surface that is *deliberately* incomplete, and a deliberately incomplete surface is one people work around.
It also made a hidden bet: that the terminal is always the right place to be, which is false the moment you
are reviewing a 62-file diff, or on a machine where the daemon is remote, or simply not in a terminal.

**Transport.** Parity is what makes the transport question real. An inspector only needs to *read*, so
server-sent events plus a page refresh is trivially sufficient. A co-equal surface must also start work,
chat, decide, cancel, and fork — which is where someone reasonably asks for a WebSocket, or GraphQL
subscriptions.

## Decision

**Both surfaces are co-equal.** Every capability exists in the TUI, the web UI, and the CLI. A capability
in one and not another is a missing handler, not a design boundary. Each surface may be *better* at
things — the terminal at latency, no-context-switch approvals, shelling out to `$PAGER`/`git difftool`, and
working over SSH; the browser at diffs, long transcripts, side-by-side comparison, and a pasteable URL —
but neither is a subset of the other. The anti-rubber-stamp constraints are surface-independent.

**Realtime is SSE for server→client and ordinary POST for client→server.** Not WebSocket. Not GraphQL
subscriptions.

The deciding argument is resumption, and it is not a matter of taste. The event log already has a
monotonic, gapless `global_seq`. SSE's `Last-Event-ID` maps onto it exactly, so a reconnecting client
resumes **at the precise event, with no server-side session state and no replay buffer to size**. That
guarantee is not something SSE happens to allow — it is something the store already provides and SSE
merely exposes. A WebSocket would require reimplementing it: sequence tracking, replay-on-reconnect,
heartbeats, and backpressure, all to arrive back at a property we started with.

Writes do not need a socket. They are low-frequency and idempotency-keyed — a message, a decision, a
cancel, a fork — and a POST returning the re-rendered fragment gets HTTP status codes, retries, caching
semantics, and `curl` for free. And one resumption story shared by the TUI, the web UI, and
`kairos logs --follow` beats three that drift.

## Consequences

**Good.** Parity costs handlers and templates, not architecture, because both surfaces are already clients
of the same API over the same socket ([0002](0002-one-process-one-host.md),
[0008](0008-terminal-is-a-client.md)) and neither holds state. Reconnection is exact rather than
best-effort, which matters most in the case you actually hit: closing a laptop lid mid-run. SSE is plain
HTTP, so it survives every proxy, needs no protocol upgrade, and is debuggable with `curl -N`.

**Bad, and accepted.** SSE is unidirectional, so anything genuinely interactive-bidirectional would be
awkward — see the revisit trigger. Browsers cap concurrent connections per origin (six on HTTP/1.1), so
the page must multiplex one event stream rather than opening one per widget; that is a real constraint the
implementation has to respect. And parity means twice the surface to keep correct, which is what
`TestUI_everyCallHasCLICounterpart` exists to make mechanical rather than diligent.

**Also bad.** Parity raises the hand-written JS budget in the web UI from roughly 200 lines to roughly 600
(command palette, keyboard model, log scroll-lock, optimistic composer echo). That is the honest cost of
not adopting a component framework, and [0007](0007-go-templates-and-htmx.md) carries its own revisit
trigger for when it stops being worth paying.

## Alternatives considered

**WebSocket.** Rejected. Bidirectionality buys nothing when writes are infrequent, and it *costs* the
resumption property described above — the one thing this system already has for free and the one thing a
laptop that sleeps needs most. A socket would also add a second connection lifecycle to reason about
alongside SSE for logs.

**GraphQL subscriptions.** Rejected outright. A schema layer, resolvers, a client library, and codegen —
reintroducing exactly the Node toolchain that [0007](0007-go-templates-and-htmx.md) removed — to serve one
user on loopback. The query flexibility it offers is not a problem anyone has here: the API's consumers are
two surfaces and a CLI we also write.

**Long polling.** Rejected: strictly worse than SSE with no compensating simplicity.

**Keep the browser as an inspector.** Rejected, above. It also mis-serves the case where the daemon is not
on the machine you are sitting at, which the runner work ([0009](0009-remote-runners.md)) makes more
likely, not less.

## Revisit when

- **A genuinely bidirectional, high-frequency stream appears** — terminal passthrough into a running
  agent's stdin, or live collaborative editing of a workflow definition. Then a WebSocket for *that one
  channel* is correct, alongside SSE rather than replacing it.
- **The one-stream multiplexing constraint starts distorting the page's structure** — if fragments begin
  sharing a stream in ways that couple unrelated screens, reconsider.
- **A second consumer appears that we do not write** — a third-party client, a mobile app — at which point
  the API's shape, not its transport, is the thing to revisit first.
