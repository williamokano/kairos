# 0007 — Go templates plus htmx, not React

**Status:** Accepted · supersedes the ancestor design's Vite + React UI stack
**Date:** 2026-08-12

## Context

The ancestor design chose Vite + React + TypeScript with a generated API client. Its own ADR named Go
templates plus htmx *"genuinely the strongest alternative, and the one most aligned with the project's
values"* and rejected it on three grounds — the v2 debugger's rich interaction, virtualised
ten-thousand-row timelines, and four dashboards wanting charts — while listing four conditions under which
it should supersede itself. It also conceded: *"if v2 never happens the trade was wrong."*

Kairos is a single binary that must stay easy to build, served on loopback to one user.

## Decision

**Go `html/template` + `//go:embed` + vendored htmx + roughly 200 lines of hand-written JS + plain CSS with
a token layer.** No Node, no npm, no lockfile, no bundler, no TypeScript, no generated client.
`go build ./...` produces the whole thing.

Two mechanics that remove the usual pain: `-tags dev` binds the template FS to `os.DirFS` and re-parses per
request, so editing HTML does not mean rebuilding the binary; and htmx is a checked-in file rather than a
CDN link, so a strict `default-src 'self'` CSP holds and the page works with no network at all.

The ancestor ADR's stated conditions have fired: client-side virtualisation existed to avoid *network*
round-trips and on loopback a `?from=` page fetch is about a millisecond; three of its four dashboards die
with the machines and pools they described, so there is no charting problem and therefore no charting
library to defer; and the v2 that justified the toolchain does not exist.

The interaction objection needs a more careful answer, because the web UI here is **co-equal** with the TUI
rather than read-only — it can fork, cancel, inject, and approve. That is still htmx-shaped: every mutation
is an API call whose response is a re-rendered fragment. What htmx is bad at is client-held state machines,
and there are none, because **no surface holds state** — both are clients of the same API
([0002](0002-one-process-one-host.md), [0008](0008-terminal-is-a-client.md)).

## Consequences

**Good, and this one is worth more than the toolchain saving.** The most important realtime rule in the
ancestor design was *"events invalidate, they do not patch — patching means reimplementing the projection
logic in TypeScript, and it will diverge."* Under htmx an SSE event triggers a **server re-render of a
fragment**, so the browser holds no model to patch and the projection logic can only exist in Go, next to
the engine that computed it. **The rule becomes structural instead of aspirational** — nobody can violate
it, rather than being asked not to.

Also good: one language, one test runner, one CVE feed; no stale-`dist` build hazard; and a reader can
follow a request from route to SQL without changing editors.

**Bad.** Rich client interactions are genuinely harder — a drag-to-reorder or a canvas view would fight the
model. Server round-trips per interaction are free on loopback and would not be over a network, which
quietly forecloses a hosted UI. And htmx pins us to a vendored version we must update deliberately.

## Alternatives considered

- **Vite + React (the inherited choice)** — a Node toolchain, a lockfile, a second dependency ecosystem, and
  a generated TypeScript client, for a single-user local page. Its "Good" column emptied out when the fleet
  did.
- **Svelte** — the same toolchain cost with less familiarity.
- **Go compiled to WASM** — a multi-megabyte payload to avoid learning htmx, with an unpleasant debugging
  story.
- **Vanilla JS with no htmx** — you end up hand-rolling fragment swapping and SSE reconnection, i.e. writing
  a worse htmx.

## Revisit when

Either: a screen is proposed whose interaction genuinely cannot be expressed as forms plus fragment swaps
(a live graph editor, a canvas), or the diff view goes unused because `delta` in `$PAGER` is better — in
which case the web surface *shrinks* rather than changing stack, and this ADR is superseded by one that
records the smaller scope.
