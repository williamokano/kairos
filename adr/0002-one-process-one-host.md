# 0002 — One process, one host

**Status:** Accepted · supersedes the ancestor design's gRPC machine protocol and its no-Kubernetes ADR
**Date:** 2026-08-12

## Context

The ancestor design split a control plane from an execution plane of registered machines, joined by a
gRPC protocol over mTLS with join tokens, heartbeats, and a per-machine agent. That split bought
isolation and multi-machine placement. Kairos gives up both, so the split buys nothing and costs a
protocol, a certificate authority, a second binary to keep in version lockstep, and a reconciliation
mechanism.

## Decision

One binary, one host. `kairos` is the daemon, the executor, the CLI, and the TUI.

Inside it, exactly one long-lived process holds the SQLite write handle and owns the run loop. Every other
invocation is a **client** over a unix socket at `~/.kairos/daemon.sock` (mode `0600`, in a `0700`
directory), authenticated by a **peer-credential check** — `SO_PEERCRED` on Linux, `LOCAL_PEERCRED` on
Darwin — requiring the connecting uid to match. No TLS, no tokens, no OIDC, no gRPC, no protobuf.

**The daemon/client split is not optional**, and the reason is not tidiness:

- SQLite has one writer, so the TUI is either *inside* the daemon or a *client of* it. There is no third
  option that does not risk `SQLITE_BUSY` storms or corruption.
- A renderer's lifetime is a terminal session, and **work must outlive it.** If the TUI owned the engine,
  closing a terminal would kill a three-hour run, and "durable workflows" would be a word.
- Two clients cannot diverge if neither executes.
- Headless is free: `kairos do "…"` in a script, a git hook, or cron needs no TTY.

`kairos` with no arguments ensures a daemon and attaches; `Ctrl-C` detaches rather than killing.

One narrowing that is easy to miss and is the most important security detail here: **the agent processes
we spawn run as the same uid and can reach that socket.** So the helper endpoint offered to agents
(`check-output`, `artifact stage`, `ask-human`) is a *separate socket with its own route table*, and a
test asserts it is a strict subset excluding `answer-task`, `publish`, and `admin`. An agent that can
approve its own gate has defeated the entire safety model.

## Consequences

**Good.** An entire surface and its bug class are deleted: no certificate rotation, no join tokens, no
heartbeat timeouts, no NAT traversal, no version-skew between two binaries, no auth code. Recovery
becomes reading a local event log rather than reconciling with remote agents. Install is one file.

**Bad, and this is the accepted cost.** The ancestor design's law that *the control plane never executes
user code* is **inverted**: this binary is a control plane that execs user code as its own children. The
trusted computing base is therefore the entire user account, and every security claim downstream is
bounded by that. It is stated in `01-architecture.md` (L6′), in `AGENTS.md` §4 rule 7, and on the
acknowledgement page in `11-limitations.md` — never softened, and never implied to be containment.

Also bad: no HA, no second instance, and nothing progresses while the daemon is down. A user-level
launchd/systemd unit shrinks that window from "terminal closed" to "lid closed", and SLA timers are gated
on engine uptime so a weekend cannot silently abandon work.

## Alternatives considered

- **Pure foreground** — engine and TUI in one process, closing it stops everything. Kills autonomous work,
  which is the premise, and forces a second process to write the same SQLite file.
- **Multi-process peers with leader election over an advisory lock** — clever, and unexplainable when it
  misbehaves. "Which process is the engine right now" is not a question a local tool should be able to
  raise.
- **Keeping gRPC for the local socket** — a `.proto`, `protoc` plus two plugins, and vendored stubs, to
  serialise between two halves of one program. HTTP/JSON over a unix socket is debuggable with
  `curl --unix-socket` for free.

## Revisit when

A second machine needs to *originate* runs rather than merely execute them, or the store gains a second
writer. Executing on another machine is a different and much smaller decision — see
[0009](0009-remote-runners.md) — and does not reopen this one.
