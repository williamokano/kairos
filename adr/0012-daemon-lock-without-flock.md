# 0012 — The daemon lock is a PID file, not `flock`

**Status:** Proposed
**Date:** 2026-08-20
**Related:** [`AGENTS.md`](../AGENTS.md) §2 · [`01-architecture.md`](../01-architecture.md) ·
[`06-durability.md`](../06-durability.md) · [`L04-daemon-api-cli.md`](../L04-daemon-api-cli.md)

## Context

`01-architecture.md` and `06-durability.md` both describe `~/.kairos/daemon.lock` as an
`flock(2)` target, so two engines can never race. Real `flock` needs `syscall.Flock`.

AGENTS.md §2 restricts `syscall` (and `os/exec`, and `golang.org/x/sys`) to
`internal/executor/local` alone — "every child process in this system is born in one package,
so that every child is recorded before it exists and killable from the event log alone" — and
that package does not exist until L06. L04 (daemon: API + SSE + CLI) needs a working daemon
lock now, three build documents before `internal/executor/local` is written.

## Decision

**`kairos serve`'s boot sequence claims a PID file, not a kernel advisory lock**, with a
socket-dial liveness probe to detect and clear a stale lock a `kill -9`'d daemon left behind:

1. Dial `daemon.sock`. A response means a live daemon is already serving; refuse to start,
   naming the PID recorded in `daemon.lock`.
2. No response (socket missing or connection refused) means any existing `daemon.lock`/
   `daemon.sock` are stale — their owner died without running its deferred cleanup. Remove both.
3. `os.OpenFile(daemon.lock, O_CREATE|O_EXCL|O_WRONLY, 0600)` claims the lock atomically. A
   second daemon racing step 3 loses (`EEXIST`) and refuses to start. This closes the
   check-then-act window steps 1–2 leave open, without a kernel lock.
4. Write `pid\nboot_time\n` to the lock file.

This lives in `cmd/kairos/serve.go`, not `internal/cli`, for the same reason `internal/api`
does — see decision #4 in `L04-daemon-api-cli.md`.

## Consequences

**Good.** No early, unjustified widening of `internal/executor/local`'s import boundary to a
package (the daemon's own boot sequence) that has nothing to do with child-process auditing.
The scheme gives the same `kill -9` resilience real `flock` would: a crashed daemon's lock is
detected as stale by the socket probe, exactly the way the kernel would have released an
advisory lock automatically on process death.

**Bad, and accepted:** a small TOCTOU-adjacent window remains between step 1 (dial) and step 3
(claim) if two `kairos serve` invocations race within it — but step 3's `O_EXCL` is what
actually decides the winner; steps 1–2 are only an optimization that avoids stepping on a live
daemon's stale-looking-but-real socket file mid-startup. Two simultaneous `kairos serve`
invocations always converge to exactly one live daemon, never zero, never two.

**Bad:** this is a real, intentional deviation from what `01-architecture.md`/`06-durability.md`
literally say ("flock"). If a reader of those documents assumes the lock is kernel-enforced
across *all* processes touching `~/.kairos` (not just other `kairos serve` invocations), that
assumption is now wrong. Mitigated by this ADR and by `L04-daemon-api-cli.md`'s Documented
decisions section citing it.

## Alternatives considered

**Widen `internal/executor/local`'s exemption to cover `internal/cli`/`cmd/kairos` for this one
call.** Rejected: the exemption's whole point is "every child process is born in one package";
a daemon lock isn't a child process, so admitting it would blur the boundary's meaning for no
benefit, and every future reader would need to remember why. Precision on this rule specifically
protects L06's reaping and orphan-detection design.

**Scaffold a minimal `internal/executor/local` now, just for `Flock`.** Rejected: it would ship
a package L06 owns in full, ahead of that document, with a one-method surface that has nothing
to do with what the real package will look like — the definition of building ahead AGENTS §7
forbids.

**Skip the lock entirely for L04 and revisit at L06.** Rejected: `kairos serve` with no lock at
all means two daemons can run concurrently, both holding `MaxOpenConns(1)` writer connections
to the same SQLite file — each individually safe, but the *pair* defeats the single-writer
guarantee `06-durability.md` and L02 depend on. That is a correctness regression, not a
deferred nicety.

## Revisit when

`internal/executor/local` exists (L06) and a narrow, reviewed change routes the daemon lock
through it instead — at that point this ADR is superseded, not edited, and the boot sequence in
`cmd/kairos/serve.go` becomes a one-function-call change. Concretely: revisit when L06 lands.
