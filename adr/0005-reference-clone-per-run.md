# 0005 — A `--reference` clone per run, never a worktree, never your checkout

**Status:** Accepted
**Date:** 2026-08-12

## Context

Every run needs a working tree an agent can edit. Three properties have to hold **at once**, and the
obvious candidates each fail one of them:

1. **Cheap** — a child run in a fan-out must not cost a full network clone.
2. **Isolated** — two concurrent runs must not be able to interfere with each other.
3. **Never your checkout** — an autonomous agent with `--permission-mode acceptEdits` pointed at your
   actual working tree, with your uncommitted work in it, is the thing this project exists to avoid.

## Decision

Per repository, Kairos keeps a **bare mirror** at `~/.kairos/mirrors/<host>/<owner>/<repo>.git`, fetched in
the background. Per run, it creates the workspace with:

```bash
git clone --reference ~/.kairos/mirrors/<host>/<owner>/<repo>.git \
          ~/.kairos/mirrors/<host>/<owner>/<repo>.git  ~/.kairos/work/<runID>/repo
```

Objects are borrowed through `objects/info/alternates`; refs, index, config, and hooks are private per run.
A 200 MB repo costs about a second and almost no disk, with no network.

**Why not `git worktree`.** It is cheaper still, and it fails property 2. Worktrees share the mirror's ref
namespace and config, so: two runs collide in `refs/heads/`; an agent's `git rebase`, `git stash`, or
`git config` reaches outside its own workspace; and a mirror `fetch --prune` can delete a ref a live run is
standing on. Worktree locking metadata also makes the mirror stateful per run, which couples mirror repair
to live runs.

**The sharp edge of `--reference`, which must be handled or it bites in month two.** `git gc` in the mirror
can repack away objects a borrower depends on, producing "object not found" inside a live workspace. So
every mirror is created with `gc.auto=0` and `gc.pruneExpire=never`, and maintenance runs **only when the
event log says no non-terminal run references that mirror**. `--dissociate` is available per repo for anyone
who wants full independence at the cost of a real object copy.

Property 3 is enforced rather than documented: startup and workspace creation **refuse** when the target
is `$HOME`, `/`, a repo containing `~/.ssh`, an ancestor of `~/.kairos`, or your own checkout with
uncommitted changes.

## Consequences

**Good.** Child workspaces are near-free, so fan-out is affordable. Integration is
`git fetch <child> && git merge FETCH_HEAD` with no object transfer, because everything already shares one
object database. A retry with a fresh workspace is a fresh clone in about a second, so
`freshWorkspace: true` becomes a sane default for write nodes instead of a runtime-dependent luxury. Your
own checkout is provably untouched, which is the single line on the approval screen that makes this tool
trustable.

**Bad.** Mirror maintenance is deferred rather than automatic, so a very active repo's mirror grows until a
quiet moment. Disk is one working tree per live run. And a workspace is *not* your checkout, so an agent
cannot see your uncommitted work — which is correct, and occasionally surprising.

## Alternatives considered

- **`git worktree` off a mirror** — fails isolation, as above. This was the initial choice and was reversed.
- **A full `git clone` per run** — property 1 fails: network, time, and disk per child.
- **A CoW clone of a canonical checkout** (`clonefile`/reflink) — fast where supported, silently a full copy
  where not, and it copies `.git` state including any half-finished operation.
- **Work in place with a branch** — fails property 3 outright, and two runs on one repo cannot both hold a
  branch.

## Revisit when

Either of two observations: mirror repacking is deferred so long that `~/.kairos/mirrors` exceeds a
configured share of the disk budget, or `kairos doctor` reports a repository where `--reference` cloning
fails or is measurably slower than a plain clone.
