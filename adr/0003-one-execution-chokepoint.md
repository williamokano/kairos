# 0003 — Execution has exactly one chokepoint

**Status:** Accepted · supersedes the ancestor design's law that "runtimes are pluggable"
**Date:** 2026-08-12

## Context

The ancestor design put isolation behind a `Runtime` interface with five operations — `Allocate`, `Exec`,
`Snapshot`, `Destroy`, `Describe` — and five implementations: Firecracker, Docker, Kubernetes, SSH, and a
local dev provider. Kairos has one: OS processes on the host. An interface with one implementation is not
an abstraction, it is indirection with a documentation cost.

Worse, the *shape* is wrong rather than merely oversized. `Allocate`/`Handle`/`Opaque`/`Snapshot` models a
remote, stateful, expensive-to-create thing. A local process is none of those, and preserving the shape
would force the executor to fake `Allocate` — inventing a directory and a pgid solely to have something to
put in an `Opaque`.

## Decision

Delete the `Runtime` abstraction. Replace the law with an **import rule**, enforced by a test:

> `os/exec`, `syscall`, and `golang.org/x/sys` are importable **only** by `internal/executor/local`.

Every subprocess in the system is created by that one package, with an explicit cwd inside the owning
run's workspace, an allow-listed environment, its own process group, and a recorded event. `internal/
workspace` runs `git` **through** the executor rather than through `exec.Command`, so every `git`
invocation is a `host.command.executed` event.

Enforced by `TestArchitecture_noExecOutsideExecutor` and
`TestArchitecture_processesRecordedBeforeSpawn` (an AST check that a recorder call precedes every
`cmd.Start`).

## Consequences

**What this preserves from the deleted abstraction.** The half of "runtimes are pluggable" that actually
mattered was *"if this turns out to be the wrong bet, the blast radius is one package."* That survives
intact, and it survives without the four vestigial types.

**What it buys that the abstraction did not.** One place where the things that must happen *every* time are
implemented once:

- recording — `process.spawning` is committed **before** `fork/exec`, so a crash in the gap leaves a
  discoverable fact rather than a process nothing knows about
- identity — `(bootID, pgid, startTime)` plus an environment cookie, so reaping never signals a recycled
  pgid and kills a stranger's process tree
- reaping — the startup sweep and the cookie sweep live next to the spawn code they mirror
- confinement — the optional `sandbox-exec` / Landlock / bwrap wrapper has exactly one call site
- accounting — `Rusage`, exit-versus-signal, and OOM classification are not reimplemented per caller

**Bad.** A future second backend must be *added* rather than *plugged in*, and the import rule is a static
test rather than a compiler-enforced boundary. Deleting a seam also means the eventual interface will be
derived from two real implementations rather than designed up front — which is better, but it does mean the
first remote backend carries the design cost.

## Alternatives considered

- **Keep the five-method interface with one implementation** — pays the serialisation-shaped API cost with
  no isolation, no independent failure, and no deployability in return, and invites `if caps.Snapshot`
  branches that can never be true. Keeping `Describe()` would be worse than deleting it: an honest local
  capability struct is nine `false`s whose only function is to be ignored.
- **A consumer-defined one-method interface at the point of use** — this is in fact what testing needs, and
  it exists (`engine.ProcessRunner`, satisfied by the real executor and a fake). What is deleted is the
  *provider registry*, not the ability to substitute a fake.
- **No rule at all** — then `os/exec` appears in the workspace manager, the artifact collector, and
  eventually a gate, and the reaper is provably incomplete because nobody knows every place a child can be
  born.

## Revisit when

A second `Runner` implementation is accepted — see [0009](0009-remote-runners.md). At that point the
interface should be extracted from the two implementations that exist, and this ADR superseded by one that
records the extracted shape. Do **not** pre-extract it in anticipation.
