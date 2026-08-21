# 0014 — Real `git worktree`s for interactive sessions, not `--reference` clones

**Status:** Accepted
**Date:** 2026-08-21

## Context

ADR 0005 rejected `git worktree` for workflow runs, for three concrete reasons: two concurrent
worktrees off one repo can collide in `refs/heads/`; an agent's `git rebase`/`git stash`/`git
config` inside a worktree can reach outside it (worktrees share the origin repo's ref namespace
and config); and a mirror's `fetch --prune` can delete a ref a live worktree is standing on. That
reasoning holds completely for its own scenario: many concurrent, fire-and-forget workflow runs,
fanning out against a shared bare mirror nobody is watching interactively.

`kairos session`/`kairos do --session` is a different scenario. The user asked for it directly,
was shown ADR 0005's exact tradeoff, and chose worktrees anyway — this ADR records why that
choice is defensible here, not just permitted.

## Decision

A Session bound to a git-backed Project gets a real `git worktree add <dir> -b
kairos/session/<id> <base>`, run off the Project's own real checkout (never a bare mirror — there
is no mirror in this path at all). Each session gets its own dedicated branch, so ADR 0005's first
objection (ref collisions) does not apply: two sessions never share a branch name, by construction.

**Why the other two objections apply less here:**

- **No automatic `fetch --prune` runs against a Project's own checkout.** Nothing in this codebase
  fetches or prunes a user's real repository in the background — that machinery only exists for
  `internal/workspace`'s own bare mirrors (ADR 0005's scenario), which Sessions never touch. A
  worktree's ref can only be invalidated by the user's OWN manual git operations in their real
  checkout, which is a cost the user explicitly accepted (see Residual risk below), not a Kairos
  bug.
- **A session's worktree lives for the life of one interactive chat**, not a fire-and-forget run
  racing arbitrarily many siblings. There is exactly one live worktree per session, and the user
  is the one deciding when a session starts and ends (`kairos session start`/`kairos session
  end`) — the failure mode ADR 0005 worried about (many concurrent unattended runs stepping on
  each other) doesn't arise when there's one human driving one session at a time.

## Consequences

**Good.** A worktree is cheaper than a `--reference` clone (no new object database, no
`alternates` file, near-instant) and — the actual point — it IS the user's real repository: the
same remotes, the same config, the same object store, addressable from the user's own terminal
via `git worktree list` run in their real checkout. This is what "define the CWD a session works
in" and "start a worktree for the chat" concretely mean.

**Bad — the honest residual risk.** Property 3 from ADR 0005 ("never your checkout") is
DELIBERATELY not held here: a Session's worktree shares ref namespace and git config with the
user's real checkout. If the user manually runs `git branch -D kairos/session/<id>` or a
config-mutating command that reaches across worktrees while a session is active, that session's
work can be disrupted — this is now the user's own responsibility to manage, exactly as it would
be with any other `git worktree` they created by hand. Kairos does not, and cannot, protect a
worktree from the same repository's other worktrees the way a `--reference` clone protects a run
from every other run.

`EndSession` removes a worktree with `--force` (any uncommitted changes in it are discarded) —
that data loss is the direct, accepted consequence of a worktree being real, uncommitted,
in-progress work rather than a disposable clone; ending a session is a genuinely destructive
action for exactly that reason.

## Alternatives considered

- **`--reference` clone per Project/Session, ADR 0005's existing mechanism, unchanged** — the
  recommended, safer option; presented to the user directly, not chosen.
- **CoW clone of the Project's checkout** — same objection as ADR 0005: silently a full copy where
  CoW isn't supported, and copies `.git` state including any half-finished operation.

## Revisit when

A user reports a real collision between a live session's worktree and their own manual git
operations in the same repository — at that point, either document the discipline required to
avoid it more prominently (e.g. `kairos session ls` warning about active worktrees before a risky
git operation), or reconsider whether ADR 0005's model should extend to sessions after all.
