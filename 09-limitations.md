# 09 — Limitations

Stated as consequences, not caveats. Each entry gets a blast radius, what mitigates it (**shipped** /
**planned** / **none**), and how you would detect it. An unenforced mitigation is a claim, and a register
full of claims is worse than no register.

---

## The one page you must accept

Shown once on first run, requiring an acknowledgement string in config, then one line in the TUI status
bar forever after.

```text
──────────────────────────────────────────────────────────────────────────────
 KAIROS LOCAL — READ THIS ONCE, THEN DECIDE
──────────────────────────────────────────────────────────────────────────────
 Kairos runs AI coding agents as YOU, on THIS machine, with YOUR credentials
 and YOUR filesystem. There is no VM, no container, no sandbox by default.

 An agent, or any dependency it installs, or any instruction hidden in an
 issue body it reads, CAN:
   · read every file your user can read — SSH keys, cloud credentials,
     browser data, password-manager sessions, this database
   · write, move, and delete files outside the workspace
   · run any command, install any package, reach any host on the internet
   · exfiltrate anything it can read, over a network Kairos does not control
   · modify Kairos's own audit log

 Kairos DOES, by default:
   ✓ record every action, tool call, decision, and cost — answerably
   ✓ block secret-shaped values from entering the event log
   ✓ construct the agent's environment from an allow-list, with its own HOME
   ✓ withhold git push credentials; Kairos pushes, after gates and approval
   ✓ work in a clone, never your working checkout
   ✓ snapshot before every write step, so damage is recoverable
   ✓ scan every diff for secrets as a gate you cannot skip
   ✓ require an explicit human decision for irreversible actions IT performs
   ✓ refuse configurations that are catastrophic on their face

 Kairos DOES NOT, and cannot without opt-in guardrails:
   ✗ prevent the agent reading your credentials
   ✗ prevent or restrict network access
   ✗ prevent an agent-run shell command from doing what a shell can do
   ✗ contain a malicious dependency
   ✗ enforce policy on actions the agent takes with its OWN tools

 BEFORE YOU CONTINUE, DO THESE THREE THINGS:
   1. Turn on branch protection on every repo Kairos will touch. It is the
      only control on this list an agent on this machine cannot bypass.
   2. Scope the credentials Kairos and your agent CLIs use. One repo, short
      TTL, no cloud keys. Assume everything reachable will eventually be read.
   3. Consider a dedicated machine or user account. If production credentials
      live on this machine, run `kairos guardrails --recommend` and read it.

 To continue, add to ~/.kairos/config.yaml:
   security:
     iUnderstandAgentsRunUnsandboxedAsMe: "yes-this-machine-is-expendable"
──────────────────────────────────────────────────────────────────────────────
```

The acknowledgement is a **string, not a boolean**, and it is recorded as an event. Typing it once is the
point.

---

## The threat model, honestly

The original design had three trust tiers separated by mTLS and a kernel boundary. There is one now:

```text
┌─ ONE TRUST DOMAIN: your user account ─────────────────────────────────┐
│ kairos daemon · kairos.db · your repos · your dotfiles · your keys    │
│ agent processes · model output · repo code · dependencies · tests     │
│ npm/pip postinstall scripts · issue text · PR comments                │
└───────────────────────────────────────────────────────────────────────┘
     the only boundaries left: your OS user, and other users' data
```

Adversaries, **re-ranked** — and the re-ranking is the important output:

| | | Was | Now |
| --- | --- | --- | --- |
| A1 | Confused agent | contained to a disposable VM | **your filesystem and your git remotes** |
| **A2** | **Malicious dependency** (`npm ci` postinstall) | contained, egress-denied | **arbitrary code as you, with network. This is now the top risk.** |
| A3 | Prompt injection via issue/PR/file content | mitigated by having nothing to steal or reach | **mitigated only by data-labelling and recording, i.e. barely** |
| A4 | Compromised model endpoint | contained | as A3 |
| A5 | External attacker via webhook | real | **eliminated by default** — no listener unless you enable one |
| A6 | The operator | no separation of duties | same, and now you also approve your own gates |

In the VM design **A1** was the headline. Locally **A2** is, because `npm ci` on a repo you did not audit
is unreviewed remote code execution as your user, and it happens on ordinary *successful* runs — not on
failures.

### The guardrail menu

Four are on by default because they cost nothing and break nothing. The rest are opt-in, because you said
isolation is not required — but each is listed with what it actually buys.

| | Guardrail | Defends | Wall or bump | Default |
| --- | --- | --- | --- | --- |
| G1 | Constructed env, per-run `HOME`, no cloud/git/ssh vars | A1, A2 accidental credential pickup | bump (absolute paths still work) | **on** |
| G2 | Agent config confinement: per-actor `CLAUDE_CONFIG_DIR`, generated `--settings` at 0444, `--strict-mcp-config` | A2/A3 reaching your Slack/Jira/cloud MCP tokens; the agent rewriting its own permissions | **wall** for MCP and settings | **on** |
| G3 | No push credentials; kairos performs the push | A1 force-push, A3 exfil-by-push | **wall** | **on** |
| G4 | Workspace is a clone, never your checkout; snapshot before every write node | A1 `rm -rf`, destructive edits | **wall** for your checkout; recovery for the workspace | **on** |
| G5 | The agent CLI's own sandbox: `codex --sandbox workspace-write`/`read-only`, Claude `PreToolUse` hooks | A1, A2, A3 — filesystem writes outside the workspace, named dangerous commands | **wall**, enforced in-process pre-execution — but only as strong as that CLI's implementation, which varies by version | on for read-only reviewer nodes; opt-in for write nodes |
| G6 | Linux Landlock | A1, A2 filesystem **writes** | **wall** (kernel, unprivileged) | opt-in |
| G7 | Linux `bubblewrap` | A1, A2 reads *and* writes, plus PID/IPC namespaces | **wall** — closest thing to the old boundary | opt-in |
| G8 | macOS `sandbox-exec` with a generated profile | A1, A2 writes; with `deny network*`, **real egress denial** | wall in practice; officially deprecated, so a macOS upgrade may break it | opt-in |
| G9 | A separate OS user + ACLs | **A2 reading `~/.ssh`, `~/.aws`, the keychain** — the thing nothing else fixes | **wall**, and the only one protecting your *reads* on both platforms | opt-in; honestly, only worth it if production credentials live on this machine |
| G10 | Resource limits: Linux `systemd-run --scope -p MemoryMax=…`; macOS RSS watchdog | A1 runaway build freezing your machine | wall on Linux, **monitor** on macOS | **on where available** |
| G11 | Mandatory secret-scan gate on every diff | A3 credential exfil via commit content | wall against *shaped* secrets; nothing against encoded data | **on**, non-waivable |
| G12 | Refuse catastrophic configurations at startup | A6, and A1 disasters | wall | **on** |

**G12 in full**, because it is free and prevents the worst outcomes. Kairos refuses to start, or to create
a workspace, when: the workspace path is `$HOME`, `/`, a repo containing `~/.ssh`, or an ancestor of
`~/.kairos`; the workspace path is your current checkout with uncommitted changes; the repo has no
configured remote (nothing to hand work back to — almost always a mistake); a write-capable actor has
cloud credentials in its env allow-list without an explicit acknowledgement; or **it is running as
root**, which converts every bump above into nothing.

**On egress (G10 in the original numbering), be blunt.** Per-run netns + nftables requires root, and if
the daemon runs as root then G9's premise collapses and everything gets worse. `HTTPS_PROXY` to a local
allow-listing proxy is trivially bypassed by a direct socket, so it is **a bump that produces logs** —
some detection value, zero prevention value. Ship it as `egress: observe`, **never** as
`egress: restricted`. The one genuinely enforced option is `deny network*` in a `sandbox-exec` profile or
`--unshare-net` in bwrap, for nodes that do not need the network: reviewers on a local model, structural
gate evaluators, static analysers. That is a real, cheap win for exactly that class of node.

---

## New limitations this variant introduces

**NL-01 · An agent can read or destroy anything you can.**
*Blast radius:* the entire user account, unbounded. `rm -rf ~`, `~/.ssh/id_ed25519`,
`~/.aws/credentials`, `~/.config/gh/hosts.yml`, browser cookie databases, every other repo, any mounted
volume.
*Mitigations:* workspace-cwd + path-escape refusal (**shipped**), allow-listed env with a per-run `HOME`
(**shipped**), running as a dedicated OS user (**planned**, documented not defaulted), optional sandbox
profile (**shipped, opt-in**), policy on platform-mediated effects (**shipped**), mandatory gates
(**shipped**), human approval on irreversible effects (**shipped**).
*Detection:* **none** for anything not routed through a platform door.

**NL-02 · Cross-run interference through shared host state.**
Worker-per-run existed to isolate exactly this. Gone: one Docker daemon, global npm/pip/Go module
caches, `~/.gitconfig`, `$GOPATH`, ssh-agent, one `gh` identity, `$TMPDIR`, and listening ports. Two
concurrent runs `docker build`ing the same tag, or both binding `:3000`, corrupt each other.
**Concurrency becomes a correctness hazard, not merely a capacity question** — which is the single
biggest hidden cost of the reduction.
*Mitigations:* per-run `TMPDIR`/`XDG_CACHE_HOME`/`GOMODCACHE` in the env allow-list (**shipped**), a
`port` resource pool (**shipped**), `hostExclusive: true` on nodes touching global state, serialised by
an admission lease (**shipped**), a cold-cache verification mode (**shipped**).
*Detection:* partial — port-bind failures and Docker tag collisions are loud; cache corruption is not.

**NL-03 · The host toolchain is unversioned and drifts.**
A run is not reproducible and replay of an old run is not fully meaningful: Go 1.22 → 1.25, a `claude`
CLI that changed `--output-format`, a missing `jq` an output adapter depends on. The toolchain used to be
pinned by a rootfs digest; now it is pinned by nothing.
*Mitigations:* `kairos doctor` recorded as a `host.probed` event and referenced from `run.created`
(**shipped**), a workflow `requires:` block checked at run start with a named failure (**shipped**),
mtime+size drift detection per node execution (**shipped**).
*Detection:* version diff between the run's recorded probe and the current probe, surfaced in
`kairos compare`.

**NL-04 · Daemonising children survive the run.**
Process-group kill misses double-forked processes: dev servers, file watchers, `docker run -d`, language
servers. The old reaper had a VM to destroy; now there is nothing to destroy.
*Mitigations:* recorded pgid + startup orphan reaper (**shipped**), the environment-cookie sweep that
catches reparented processes (**shipped**), `PR_SET_CHILD_SUBREAPER` and cgroup `cgroup.kill` on Linux
(**shipped where available**), `kairos doctor --orphans` (**shipped**).
*Detection:* partial. **On macOS the cookie sweep is the whole answer, and a process that scrubs its own
environment is invisible to it. There is no equivalent of `cgroup.kill`.**

**NL-05 · Durability of state, but not of liveness. You close the laptop.**
State survives; **nothing progresses while the binary is down**, and it goes down on `Ctrl-C`, sleep, an
OS update, or battery death. Timers do not fire and pollers do not poll.
*Mitigations:* a launchd/systemd **user** service with restart (**shipped**, `kairos up --install`),
absolute wall-clock deadlines so a timer that expired during downtime fires immediately on start
(**shipped**), SLA timers gated on engine uptime so a weekend cannot silently abandon five runs
(**shipped**), polling rather than webhooks as the default inbound path (**shipped**), a startup
reconciliation report naming every wait that expired while down (**shipped**).
*Detection:* the startup reconciliation report; the gap between `occurred_at` and `recorded_at`.

**NL-06 · SQLite single-writer contention, and hostile home directories.**
WAL permits one writer. Worse: `~/.kairos` on a cloud-synced or network-mounted home (corporate macOS
with a synced home, iCloud Documents, NFS `$HOME`) produces locking failures and real corruption.
*Mitigations:* a single writer goroutine with group commit (**shipped**), logs on the filesystem rather
than in the database (**shipped**), **refusing to start when `~/.kairos` is on a non-local filesystem**
with `--store-path` as the escape (**shipped**).
*Detection:* the `kairos doctor` filesystem probe; a `SQLITE_BUSY` counter that should always read zero.

**NL-07 · Two invocations of one binary.**
Two engines advancing one run is a `(stream, sequence)` race that the unique constraint turns into
permanently halted runs.
*Mitigations:* `flock` on `~/.kairos/daemon.lock`; every subcommand is a **client** of the running
instance and starts one only if absent (**shipped**).
*Detection:* loud, by construction.

**NL-08 · Workflow definitions are arbitrary code execution.**
A `shell` actor's command, a deterministic constraint's check, and an output expression all run on the
host with your privileges. **Importing a shared workflow is `curl | sh`.** Previously these ran in a
disposable VM with restricted egress.
*Mitigations:* workflows are trusted content — say so (**shipped**); `kairos check` prints every command
a definition will execute and requires confirmation (**shipped**); a publish-time command inventory in
the log (**shipped**); signed definitions (**planned**, if they are ever shared).
*Detection:* the publish-time command inventory.

**NL-09 · The agent uses your identity.**
Leaning on the host's `gh auth` means provider-side audit logs attribute agent actions to *you*,
revocation is all-or-nothing, and the old mitigation — a GitHub App token for one repo for one hour being
a much smaller prize than a PAT — is unavailable.
*Mitigations:* a dedicated-identity default with a platform-held `GH_TOKEN` scoped per project and
distinct from your own login (**planned**); per-actor config homes (**shipped**).
*Detection:* provider audit logs — attributed to the wrong principal, which is the problem.

**NL-10 · The UI shares the process with the engine.**
A panic in a render loop could kill in-flight runs; a blocked terminal write could backpressure the event
bus.
*Mitigations:* the UI is strictly a subscriber, never in the advance path (**shipped**, enforced by an
architecture test); per-subscriber bounded channels that **drop and record** rather than block
(**shipped**); `recover` at every UI goroutine boundary, recorded as an event, never silent
(**shipped**).
*Detection:* dropped-frame and dropped-subscription counters.

**NL-11 · Snapshot and fork economics depend on the filesystem.**
APFS `clonefile` and btrfs/XFS reflink give O(1) clones. ext4 without reflink, or an external
exFAT/NTFS drive, degrades to full copies — and a 5 GB monorepo × 3 children fills a laptop disk and
takes minutes. `freshWorkspace` retries silently become expensive rather than cheap.
*Mitigations:* probe CoW support at startup by *attempting a clone*, and report the real duration rather
than pretending it was instant (**shipped**); warn at publish for snapshot-heavy workflows on a non-CoW
filesystem (**shipped**); a git-object snapshot layer that works everywhere (**shipped**); low
`keepSnapshots` default (**shipped**).
*Detection:* the `kairos doctor` filesystem row; snapshot duration and disk-delta metrics.

**NL-12 · No filesystem quota enforcement.**
A runaway `docker build` can fill your disk mid-node. The original design's argument for
kernel-enforced quotas — a 30-second poll loop is too slow — was correct, and there is no ZFS quota, no
btrfs qgroup, and no `prjquota` here.
*Mitigations:* a 5-second disk watchdog that fails the node (**shipped**), `SIGSTOP` on the process group
at a hard threshold so you can intervene rather than losing the machine (**shipped**), `minFreeAbsolute`
bounding kairos by the *host's* health rather than only its own quota (**shipped**), opt-in cgroup
`io.max`/`memory.max` on Linux (**shipped where available**). **On macOS: none.**
*Detection:* the watchdog, after the fact.

**NL-13 · The audit log is not tamper-proof.**
The agent can write `~/.kairos/kairos.db` unless G6–G9 are enabled.
*Mitigations:* `guardrails-untouched` covers `~/.kairos/**` in the diff gate (**shipped**), refusing to
start when the state dir is inside the workspace root or vice versa (**shipped**), backups to a location
the agent cannot write (**planned**), signed event chains (**planned** — and this moves from
nice-to-have to the honest fix for this variant).
*Detection:* `kairos db verify` integrity check.

---

## What got worse

| | |
| --- | --- |
| **Prompt injection** | The mitigation table collapses from seven shipped rows to three. Kernel isolation, default-deny egress, and least-privilege short-lived tokens all disappear. The old strategy was *"make a successful injection not matter much"*; that strategy is now largely unavailable, and G5–G9 are what buy part of it back. |
| **Exfiltration through allowed channels** | Degenerates: there is no allowlist, so every channel is allowed. Merges into NL-01. |
| **Logical session resume** | Becomes universal rather than runtime-conditional — `exact` resume was hypervisor-only. So the rule *"anything worth carrying must be typed output, never transcript"* becomes mandatory rather than advisory. |
| **Supply chain** | No images to pin, so you trust the host's entire installed toolchain plus whatever `npm ci` pulls **directly onto your machine, with your credentials present**. |
| **Memory bounds concurrency** | Survives and changes character: the binary now competes with your IDE, browser, and Docker Desktop for the same RAM, and there is no reservation accounting to arbitrate. |
| **Malicious or mistaken operator** | Unchanged in text, but the operator and the victim are now the same person's laptop, so the blast radius includes personal data. And you approve your own gates — watch the 100%-approval signal. |

## What got better, or disappeared

- **Uncommitted work loss.** Three of its four original causes are gone: machine loss, worker release,
  and reclaim-during-wait. The workspace directory survives shutdown, reboot, and crash.
- **A workspace pinning a run to a machine.** No machines, no affinity queueing.
- **Cloud bursting degrading isolation.** No bursting.
- **Log loss under backpressure.** Nearly gone: no network hop, and the file is the buffer.
- **Fork retention windows.** Git objects are content-addressed and cheap, so the fork window becomes
  **unbounded** — you can fork a run from three months ago, which the distributed design could not offer
  at any price.
- **The external attacker.** No listener by default.
- **An entire auth surface** — no tokens, no TLS, no OIDC, no CORS, no tunnels. Replaced by unix file
  permissions.
- **Multi-tenancy** moves from "deferred" to a permanent non-goal.

## What is foreclosed rather than deferred

**The multi-machine growth path.** The original design promised *"useful on one machine, unchanged on
twenty"* — adding a machine was meant to be registration, not reconfiguration. This variant trades that
away for zero setup, and re-adding it is a rewrite of admission, workspace ownership, and the event bus,
not a configuration change. Also foreclosed: the capability-advertisement mechanism that per-machine
verification was built on.

Saying otherwise would be a lie in a document, and a lying document is worse than a missing one.
