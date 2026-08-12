# 0011 — Outbound polling is the default ingestion mechanism; webhooks are opt-in and BYO-tunnel

**Status:** Accepted
**Date:** 2026-08-12
**Related:** [0002](0002-one-process-one-host.md) · [0009](0009-remote-runners.md) ·
[`08-triggers.md`](../08-triggers.md) · [`14-connectors.md`](../14-connectors.md)

## Context

Kairos ingests work from sources it does not control: GitHub issues, Jira, Linear, email, Telegram, WhatsApp,
calendars. There are two ways to learn that something happened.

**Webhooks** are the industry default and the reflex answer. They are also the answer that assumes a
publicly reachable HTTPS endpoint — which is precisely what a laptop, the target of this entire design
([0002](0002-one-process-one-host.md)), does not have. Providing one means a tunnel, a public DNS name, a
TLS certificate, an HMAC shared secret per source, a replay window, and a listener that is by definition
reachable by anyone on the internet who finds it.

**Polling** is assumed to be the compromise you accept when you cannot have webhooks. That assumption turns
out to be wrong for the sources that matter here, and the reason is worth stating precisely rather than
discovering later: the best mechanisms these APIs offer are **outbound connections that behave like push**.
Telegram's `getUpdates?timeout=50` is a long-poll held open by Telegram, delivering in well under a second.
IMAP has `IDLE`, where the server pushes over a connection the client opened. Gmail's `history.list` returns
an exact delta since a cursor for one quota unit. Three of the four best options are outbound, and two of
them are genuinely instant.

So the local-first constraint and the personal-assistant use case do not conflict. They happen to want the
same thing.

## Decision

**Outbound polling — including long-polling and server-push-over-an-outbound-connection — is the default
and only required ingestion mechanism for every connector.** No connector may be *implemented only* as a
webhook if any outbound mechanism exists for that source.

**Webhooks remain supported, opt-in, and bring-your-own-tunnel.** Enabling a webhook source is what causes
the daemon to bind a TCP port at all; with none configured, the only listener is the unix socket. When
enabled, the existing rules in [`08-triggers.md`](../08-triggers.md) apply unchanged: HMAC verified before
parsing, failures dropped with a counter and no body that reveals whether the source exists, and
`trigger_dedupe` absorbing redelivery. Kairos does not create, manage, or supervise the tunnel, and says so
in the CLI output rather than implying otherwise.

Cursor state stays owned by the daemon, never the plugin, for both mechanisms.

## Consequences

**Good, and the security consequence is the largest.** With no listener, the external attacker leaves the
threat model entirely — there is nothing to reach. That removes an adversary rather than mitigating one,
which is a category of win the rest of this corpus rarely gets to claim. It also deletes a stack of
machinery: no tunnel client to vendor and keep patched, no certificate lifecycle, no public DNS, no
per-source shared secret to rotate, no replay window to tune, and no "is my endpoint up" failure mode
distinct from "is my daemon up".

**Good: it composes with the laptop.** A closed lid is not an incident; it is Tuesday. A poller that resumes
from a stored cursor handles a fourteen-hour gap by design, whereas fourteen hours of undelivered webhooks
are simply gone — most providers retry for minutes, not hours, and the ones that do not retry at all lose
the event silently. Polling makes downtime a *latency* problem instead of a *loss* problem, which is the
same trade the durable event log makes everywhere else.

**Bad: latency, where the API is a plain poll.** Gmail at 45 s and calendar at 5 m are genuinely slower than
a push. For a digest-and-label assistant that is invisible; if a use case ever needs sub-second email, this
decision is what stands in the way. Telegram and IMAP `IDLE` do not have this problem.

**Bad: quota and battery.** A poll costs a request whether or not anything happened. Mitigated by
conditional requests (`ETag`/`If-None-Match`, where a `304` costs no quota), by delta APIs rather than
list-and-diff, and by per-connector intervals rather than one global number — but a laptop on battery is
still making requests. Long-polling is better than short-polling on both counts and should be preferred
wherever offered.

**Bad, and accepted as a real gap: one connector cannot comply.** The WhatsApp Business Cloud API offers no
outbound mechanism at all, so it *requires* a webhook and therefore a tunnel. This is one of several
reasons [`14-connectors.md`](../14-connectors.md) marks WhatsApp the weakest connector, ships it as a plugin
rather than compiled in, and recommends Telegram instead. The decision is not weakened by the exception,
because the exception is visible, documented, and opt-in — it is not a default anyone gets by accident.

## Alternatives considered

**Ship a tunnel client** (embed `cloudflared`, `ngrok`, or Tailscale Funnel). Rejected. It means vendoring a
network daemon into a single-binary tool, plus an account with a third party, plus a rotating credential,
plus a support surface every time that vendor changes something — to reduce email latency from 45 seconds to
2. It also punches a hole from the public internet to a process that, by design, runs unsandboxed agents
with your credentials (NL-01). The cost/benefit is not close.

**A Kairos-operated relay** that clients connect to outbound and which receives webhooks on their behalf.
Rejected outright, and it is the most tempting of the alternatives because it would work well. It makes this
a hosted service: an account system, an availability obligation, a privacy position on relaying other
people's email metadata, and a second threat model. The premise of the entire design is that there is no
service. This would quietly undo it.

**Require a VPS or a homelab box with a public address.** Rejected. It converts "install a binary and it
works" into an infrastructure project, violating L13′. It is also self-defeating: the whole point of the
reduction was that the first user has one machine. Note that [0009](0009-remote-runners.md) lets you *add*
machines for execution — but adding one must never become a prerequisite for ingestion.

**Webhooks-first with polling as a fallback.** Rejected as the worse ordering of the same two mechanisms.
Every connector would carry two code paths, two dedup stories, and two failure modes; the fallback would be
the one that runs in practice and the one that is least tested. Better to have polling be the path that
always works and webhooks be a documented optimisation for the cases that ask for it.

## Revisit when

- **A connector's outbound mechanism is withdrawn**, and it matters. Concretely: Telegram deprecating
  `getUpdates` in favour of webhook-only delivery, or Google removing `users.history.list`. That converts an
  exception into the rule and forces the tunnel question properly.
- **A use case needs sub-second latency on a poll-only source**, and someone can state it as a requirement
  rather than a preference — for example a workflow that must respond to an inbound email inside ten
  seconds. Today no described use case needs this.
- **Measured battery or quota cost becomes a complaint.** The observable trigger: a connector's poll requests
  exceed a configured daily ceiling without producing work, which `kairos status` can report. That argues for
  longer intervals or `IDLE`, not for webhooks — but it is the point at which the numbers deserve a look.
