# 0006 — Workspace snapshots are git refs plus optional CoW trees

**Status:** Accepted · supersedes the ancestor design's VM and ZFS snapshots
**Date:** 2026-08-12

## Context

Forking a run at node *N* and retrying a node with a fresh tree both require restoring a workspace to a
previous instant. The ancestor design had two mechanisms: a VM memory-plus-disk snapshot, and ZFS/btrfs
dataset snapshots with a filesystem-enforced quota. Kairos has no hypervisor and cannot require a
particular filesystem.

## Decision

Two layers, taken at node boundaries where the workspace is writable, and **the layer actually obtained is
recorded** rather than assumed.

**Layer 1 — git objects. Always available.** Build a snapshot commit out of band, touching no user-visible
state — not `HEAD`, not the index, not the branch, not the reflog:

```bash
GIT_INDEX_FILE=$tmp/idx git -C $repo add -A
tree=$(GIT_INDEX_FILE=$tmp/idx git -C $repo write-tree)
sha=$(git -C $repo commit-tree $tree -p HEAD -m "kairos @pre-implement-1")
git -C $repo update-ref refs/kairos/runs/<runID>/<seq> $sha
```

Deliberately **not `git stash`**, which mutates a working tree the agent may be mid-write in.

**Layer 2 — a copy-on-write tree clone, where the filesystem supports it.** `clonefile(2)` on APFS,
`btrfs subvolume snapshot`, `ioctl(FICLONE)` on btrfs/XFS with reflink. Captures gitignored build state
(`node_modules`, `target/`, `.venv`) that layer 1 cannot. Elsewhere: a `tar.zst` minus declared caches, or
skipped with a recorded reason.

**Detection is by probe, not by `statfs`** — filesystem type does not tell you whether reflink is enabled,
and a wrong assumption turns milliseconds into minutes silently. Probe once at startup by attempting a
clone; report the real duration rather than pretending it was instant.

What a fork therefore restores: the event prefix **exactly**; node inputs and outputs **exactly** (they are
recorded facts, not recomputations); the workspace **approximately** — tracked and untracked-non-ignored
files exactly, but not gitignored build state, file mtimes, empty directories, or xattrs; the agent's
session **not at all**; and external effects already applied **never**. If no snapshot exists at the
requested sequence the fork is **refused**, not silently drifted.

## Consequences

**Good.** Content-addressed and deduplicated across every snapshot of every run, so five hundred runs cost
the deltas rather than five hundred trees. Restoring is a checkout. It is **inspectable with tools you
already have**: `git diff refs/kairos/runs/A/5 refs/kairos/runs/A/7`, and
`git log --graph --all --glob='refs/kairos/**'` shows the tree of every attempt any agent ever made on your
repo — a free and genuinely novel artifact. And because git objects are cheap, **the fork window becomes
unbounded**: you can fork a run from three months ago, which the ancestor design could not offer at any
price.

**Bad.** Layer 1 misses build state, so a fork of a node whose value was a warm `target/` pays for a rebuild.
Layer 2's cost is filesystem-dependent, so `freshWorkspace` retries are cheap on APFS and expensive on
ext4-without-reflink. `git clean -fdx` in a fresh-workspace retry will delete forty minutes of `npm ci`
unless cache paths are declared and excluded — which is the trap that makes layer 2 worth having.

And the honest limitation: a snapshot never restores a running process. Every fork cold-starts a new agent
session seeded from a context digest, because you cannot rewind a model conversation to turn 14 of 41.

## Alternatives considered

- **VM snapshots** — no hypervisor; also the only mechanism that could have restored a session, and it is
  gone with the rest of the isolation story.
- **ZFS/btrfs datasets as a requirement** — cannot be required of a user's laptop. Used opportunistically.
- **`tar` per snapshot as the only layer** — an opaque blob, no dedup, no `git diff` between snapshots.
- **Nothing; re-derive from the base ref** — correct for read-only nodes and workflows that commit anyway,
  but it kills fork's value, because the interesting fork point is mid-implementation with uncommitted work.

## Revisit when

`kairos doctor`'s copy-on-write probe fails on the primary development machine, or snapshot duration
exceeds **5 seconds p95** or the disk delta exceeds **10 MB** for an unchanged tree. Either means layer 2 has
silently degraded to full copies and the snapshot-heavy defaults need reconsidering.
