# 05 — Gates

With no isolation, **gates carry the entire safety budget.** They are the mechanism that stops an agent
skipping the rules it was told to follow — including when it isn't trying to, which is the common case:
an agent working hard to satisfy *"make the tests pass"* will find every path that makes them pass.

---

## The invariant

> **Every gate is evaluated by the engine process, or by a direct child of it, after the actor's process
> has exited, with the result appended to the event log before any edge is computed.**

Four clauses, each blocking a specific escape:

1. **After the actor's process has exited.** No gate ever runs inside the agent's session, tool call,
   or shell. It cannot observe, wrap, mock, or intercept one. It cannot `alias golangci-lint`, because
   the engine resolved that binary to an **absolute path at preflight** and execs that path with a
   constructed `PATH`.
2. **By the engine.** The gate schedule is engine data, read into memory before the run started. The
   agent has no code path that reaches it.
3. **Result appended before edges.** The edge taken is computed *from* `constraint.evaluated`. There is
   no path from actor output to `$succeed` that does not pass through gate evaluation.
4. **Order is fixed:** output-schema validation → gates in declared order → `on` edges.

Placement and cost per kind:

| Kind | Where | Cost | Takes a permit? |
| --- | --- | --- | --- |
| `expr` | in-process, over typed JSON | µs | no |
| `file` | in-process `os.Stat` + globs, rooted at the workspace | µs | no |
| `regex` | in-process over an engine-parsed diff | ms | no |
| `git-diff` | `git` child with fixed argv, assertions in-process | ~10ms | no |
| `command` | child process, cwd = workspace | seconds–minutes | **yes, `cpu.heavy`** |
| `coverage` | `command` + numeric extraction in Go | seconds–minutes | **yes, `cpu.heavy`** |
| `judged` | an actor invocation, fresh session, different actor | a model call | yes, a model slot |

Locally, deterministic gates are roughly **three orders of magnitude cheaper** than in the original
design, because the toolchain is installed and the caches are warm: a lint gate that cost 90s in a cold
VM costs 3s. That changes workflow design — you can afford a gate after *every* node, which is exactly
the direction the `structural → deterministic → judged` ladder points.

---

## The seven kinds

### `expr` — a structural assertion on typed output

```yaml
- id: plan-covers-requirements
  kind: expr
  severity: critical
  check:
    expr: "all($.output.requirements[*].id, . in $.output.tasks[*].satisfies[])"
  message: "Requirement {{ .failing }} is not claimed by any task"

- id: findings-were-addressed
  kind: expr
  check:
    expr: "all($.outputs.review.findings[?(@.severity=='high')].id, . in $.output.addressedFindings[])"
```

In-process, free, needs no workspace, and **unbluffable**. This is the highest-leverage kind and the
most under-used. It exists *only* because `outputSchema` is mandatory — which is why that stays
non-negotiable.

### `command` — the exit code of a host command

```yaml
- id: lint
  kind: command
  severity: high
  check:
    command: ["golangci-lint", "run", "--new-from-rev={{ .base }}", "--out-format=json"]
    workdir: backend              # relative to the workspace; absolute paths REJECTED
    expect: { exitCode: 0 }
    timeout: 5m
    findingsFrom: { format: golangci-json }
  waivable: false
```

Mechanics: take a `cpu.heavy` permit → resolve the binary to its preflight absolute path → exec with
`Dir = <workspace>/backend`, `Setpgid`, `Nice: 10`, stdout/stderr to capped ring buffers (64 KiB tail as
evidence, full output as an artifact) → compare exit code → run the findings adapter → append
`constraint.evaluated{result, exitCode, durationMs, evidence, findings}` → release.

`{{ .base }}` is the run's base ref injected by the engine — **not** a hardcoded `origin/main`, which
breaks on any non-main base.

### `file` — exists / absent

```yaml
- id: migrations-reversible
  kind: file
  check:
    forEach: { changedFiles: "migrations/**/*.up.sql" }
    exists: "{{ trimSuffix .item \".up.sql\" }}.down.sql"

- id: no-vendored-blobs
  kind: file
  check: { absent: ["**/*.pyc", "vendor/**", "node_modules/**"] }
```

Compare the shell version this replaces:
`sh -c "for f in $(git diff --name-only origin/main -- migrations/ | grep '\.up\.sql$'); do test -f …"`.
That depends on the host's `sh`, `grep`, word-splitting, and `origin/main` existing. On a tool where the
host is *whatever the user has*, **host-fragile constraints are the top source of false failures** —
so reimplementing the corpus's shell-pipeline constraints as first-class in-process kinds is the single
best local improvement to the gate system.

### `regex` — over the engine's own diff

```yaml
- id: no-todos
  kind: regex
  check:
    over: added-lines            # added-lines | diff | files | file-contents
    absent: '(TODO|FIXME|XXX|HACK)\b'
    exclude: ["**/*_test.go", "docs/**"]

- id: no-focused-tests
  kind: regex
  check: { over: added-lines, absent: '\b(t\.Skip|it\.only|describe\.only|fdescribe)\b' }
```

`over: added-lines` means the engine runs `git diff --unified=0 <base>..HEAD`, parses the unified diff
**in Go**, and applies Go's `regexp` to `+` lines only. No `grep`, no BSD-vs-GNU flag divergence, no
exit-code inversion in `sh -c`. Findings carry `file:line` automatically, which the shell version could
not.

### `coverage` — a numeric threshold

```yaml
- id: coverage
  kind: coverage
  check:
    command: ["go", "test", "./...", "-coverprofile=$KAIROS_DIR/c.out"]
    then:    ["go", "tool", "cover", "-func=$KAIROS_DIR/c.out"]
    capture: { from: stdout, regex: 'total:\s+\(statements\)\s+([0-9.]+)%', group: 1 }
    expect:  { min: 80 }
    baseline: git              # optional: must not decrease vs the base ref
  message: "Coverage {{ .value }}% is below {{ .min }}%"
```

The comparison is a typed float in Go, not a regex like
`stdoutMatches: 'total:.*\s(8[0-9]|9[0-9]|100)\.\d%'` — which silently passes `85.3` *and* silently
fails `100%`. **Threshold gates must compare numbers.**

### `git-diff` — scope and shape

```yaml
- id: scope-respected
  kind: git-diff
  severity: critical
  check:
    pathsAllowed: "$.outputs.interpret.allowedPaths"     # dynamic, from the classifier
    pathsForbidden: [".kairos/**", ".github/workflows/**", "infra/production/**"]
    maxFiles: 40
    maxLines: 1500
    noBinary: true
    mustTouch: ["**/*_test.go"]        # a change with no test change is rejected
  waivable: false

- id: guardrails-untouched            # BUILT-IN, always merged, non-removable
  kind: git-diff
  check: { pathsForbidden: [".kairos/**", ".github/**", ".git/hooks/**", "~/.kairos/**"] }
  waivable: false

- id: clean-tree                      # BUILT-IN, auto-attached to `workspace: read` nodes
  kind: git-diff
  check: { dirty: false, staged: false }
  message: "A read-only node modified the working tree"
```

`clean-tree` is the local replacement for a read-only mount. The distributed design made a reviewer
*structurally incapable* of editing code by mounting the workspace read-only; a directory cannot cheaply
be made read-only on macOS. So the engine snapshots `git status --porcelain` + `git rev-parse HEAD`
before a `workspace: read` node and asserts equality after. A reviewer that "helpfully fixed" what it
found is **caught and its change discarded, while the finding survives**.

Weaker than a mount — the damage happens, then is detected and reverted — but it preserves the property
that matters: *the finding is not lost*. And it is honest about the difference.

Where a stronger version is available for free, use it: `codex --sandbox read-only` for reviewer nodes
is enforced by the agent's own Seatbelt/Landlock, before the tool call.

### `judged` — an actor's opinion, with four rules that don't bend

```yaml
- id: security-review
  kind: judged
  severity: critical
  check:
    actor: reviewer-security
    lens: "authz, injection, secrets handling, SSRF, unsafe deserialisation"
    framing: refutation          # "try to find a violation; default to inconclusive"
    outputSchema: { $ref: schemas/judgement.json }   # evidence: minItems 1
    quorum: { of: 2, from: [reviewer-security, reviewer-security-codex] }
  waivable: false
```

- **Evidence required, or the verdict is `inconclusive`** — and `inconclusive` does not pass. This is
  the whole trick; without it a judged constraint is a vibe.
- **The judge is never the session under judgement**, enforced mechanically: the engine refuses to pass
  the implementer's session into a judged gate, and publish-time validation rejects a `judged`
  constraint whose actor equals the actor of any node it gates.
- **`framing: refutation`** — asking a model to find a violation and default to inconclusive produces
  far fewer false passes than asking it whether the code is good.
- **Quorum across two different CLIs** is the local answer to "different lenses, not duplicate
  judges", and it is cheap because both are already installed.

---

## The constitution

```yaml
# ~/.kairos/projects/acme-backend/constitution.yaml    ← AUTHORITATIVE
extends: kairos/baseline            # compiled into the binary
gates:
  after-plan:      [plan-covers-requirements]
  after-implement:
    strategy: all                   # all (local default) | fail-fast
    order: [build, lint, no-todos, no-secrets, coverage, scope-respected, guardrails-untouched]
  before-finalize: [architecture-fit, security-review]
mandatoryGates: [after-implement]   # merged into EVERY workflow; not removable
```

Resolution order, and the ordering is the point:

1. **`kairos/baseline`** — compiled into the binary. Contains `guardrails-untouched`, `no-secrets`,
   `clean-tree`. Cannot be removed, only extended.
2. **`~/.kairos/projects/<project>/constitution.yaml`** — authoritative, and **outside every
   workspace**. The agent's `--add-dir` is the workspace; this file is not in it. On a single machine
   this is nearly free and it is the strongest available control.
3. **`<repo>/.kairos/constitution.yaml`** — convenient and PR-reviewable, merged in, but **loaded and
   content-hashed before the run starts and never re-read**, and covered by `guardrails-untouched`.
   Editing it mid-run changes nothing, and the edit itself fails the gate.

### Why this holds against an agent that is trying to pass

| Escape | Why it fails |
| --- | --- |
| delete the constraint | the authoritative copy is outside its directory; the in-repo copy is hash-pinned at run start and diff-gated |
| weaken CI | `.github/**` is in `guardrails-untouched` |
| skip the gate | the schedule is engine data, evaluated after its process exits |
| fake the result | `constraint.evaluated` is appended by the engine from the child's actual exit code |
| shadow the tool | absolute paths resolved at preflight, constructed `PATH`, no shell in between |
| install a git hook to fake the diff | `.git/hooks/**` is guarded, and gate-relevant `git` runs with `core.hooksPath=/dev/null` |
| grant itself a waiver | `waiver.grant` is deny-tier for every non-human principal. A waiver is an event with an author, a reason, and an expiry — and the author is always the human |

Defence in depth, labelled best-effort and *not* the enforcement: `--disallowedTools`,
`codex --sandbox workspace-write`, `--add-dir` scoping. Those raise the cost of an accident. **The
enforcement is that the engine, not the agent, runs the check.**

`strategy: all` is the local default. One round trip with six findings beats six round trips — and
locally `all` also wins on wall clock, because there is no per-check VM boot: the three CPU-heavy checks
overlap up to `cpu.heavy`, and the four in-process checks are instantaneous. Typical: ~90s for the whole
implement gate on a Go service.

---

## Policy: effect permissions, not RBAC

With one human user, RBAC has nothing to describe. Delete principals, teams, roles, resource patterns,
and `policy simulate`. **Keep effect-level permissions and nothing else.** Policy answers exactly one
question: *may this actor cause this outward mutation?* Constraints answer *is this work acceptable?*
Both still exist, and substituting one for the other remains a mistake.

```yaml
# ~/.kairos/policy.yaml — shipped default; `kairos policy show` prints the effective merge
default: deny                      # absence of a grant IS a denial

effects:
  # ── silent, logged ──
  git.commit:         { allow: "*", paths: ["!.kairos/**", "!.github/**"] }
  git.branch.create:  { allow: "*", match: "kairos/**" }
  fs.write:           { allow: "*", paths: ["!.kairos/**", "!~/**"] }
  gh.read:            { allow: "*" }
  model.invoke:       { allow: "*" }

  # ── requires a keystroke ──
  git.push:           { confirm: once-per-run, match: ["!main", "!master", "!release/*"] }
  gh.pr.create:       { confirm: each }
  gh.pr.comment:      { confirm: once-per-run }
  jira.transition:    { confirm: each }
  shell.exec:         { confirm: each, match: ["rm *", "sudo *", "npm publish*",
                                               "docker *", "kubectl *", "terraform *"] }

  # ── never ──
  git.push.force:     { deny: "*", reason: "Force-push destroys history. Do it yourself." }
  gh.pr.merge:        { deny: "*", reason: "Agents propose; humans dispose." }
  gh.workflow.edit:   { deny: "*", reason: "An agent that can edit CI can pass its own CI." }
  gh.release.create:  { deny: "*", reason: "Releasing is a human decision." }
  deploy.*:           { deny: "*", reason: "No deploys from a local autonomous run." }
  terraform.apply:    { deny: "*", reason: "No infrastructure mutation." }
  waiver.grant:       { deny: "*", reason: "A gate an agent can waive enforces nothing." }
  fs.delete:          { deny: "*", paths: ["~/**", "/**", "!{{ .workspace }}/**"],
                        reason: "An agent may only delete inside its own workspace." }
```

Three tiers, exhaustive: `allow` (happens, logged as `effect.applied`), `confirm`
(`each` | `once-per-run` | `once-per-session`), `deny` (outcome `denied`, which routes separately from
`failure` — usually to a human).

`deny` always beats `allow` regardless of order, and **`reason` is mandatory on every deny** — because
the person who will hit that denial at 11pm and work around it is you.

### The honest limit, stated plainly

> **Policy is a wall for actions kairos performs, and a request for actions the agent performs with its
> own tools.**

`gh.pr.merge: deny` genuinely stops the *effect*. It does not stop `bash -c 'gh pr merge 412 --squash'`,
because that path does not go through kairos at all. Three responses, in order of value:

1. **Remove the capability** — no `GH_TOKEN` in the env, a dead push URL, no credential helper. Then
   `gh pr merge` fails with an auth error rather than a policy error, and *that* is a wall. See
   [`04-agents.md`](04-agents.md).
2. **Use the CLI's own pre-tool enforcement** — a `PreToolUse` hook in a kairos-generated settings file
   at 0444 outside the workspace, denying `Bash` commands by pattern *before* they run, emitting a
   kairos event. Real, cheap, and bypassable only by editing a file the agent cannot write.
3. **Branch protection on the git host.** Server-side, outside your machine entirely, and therefore the
   only control here that an agent on your box cannot touch. **This belongs on the first-run checklist,
   not in a footnote.**

---

## Confirming a destructive effect

```text
1. Node `pr` reaches an effect with tier `confirm`.
2. The engine renders a preview, dry-running where possible:
     action   gh.pr.create
     command  gh pr create --base main --head kairos/run_01J8 --title "…" --body-file …
     target   github.com/acme/backend
     blast    14 files, +412/-38, 1 new dependency (golang.org/x/sync)
     gates    build ✓  lint ✓  no-todos ✓  coverage 84.1% ✓  scope-respected ✓
     cost     $4.12 spent so far
     revert   gh pr close <n>          ← the declared compensation
3. Append effect.confirmation.requested{effectID, action, args, preview, idempotencyKey}
   → insert a human task → RELEASE ALL PERMITS → the node enters Waiting.
   The engine is now idle. A desktop notification fires.
4. TUI:  ┌ CONFIRM ─────────────────────────────────┐
         │ gh.pr.create → acme/backend              │
         │ [d] diff  [c] full command  [g] gates    │
         │ [y] yes   [n] no   [a] yes, all this run │
         └──────────────────────────────────────────┘
   or headlessly:  kairos approve run_01J8 --effect pr --yes
5. y → effect.confirmed{scope: once}   a → {scope: run}   n → effect.declined → on.denied
   timeout (default 24h, onTimeout required) → declined
6. effect.attempted{idempotencyKey}   ← BEFORE the call
   exec the builtin with a credentialed env
   effect.applied{externalRef: "acme/backend#418"} | effect.failed | effect.unknown
```

Properties worth preserving deliberately:

- **A confirmation wait is an ordinary wait**: zero processes, zero permits, survives a restart. A run
  parked on a confirmation for two days costs one directory.
- **`effect.attempted` precedes the call**, so a crash mid-`gh pr create` leaves `effect.unknown` and
  startup *probes* GitHub by idempotency key instead of blindly retrying and opening a second PR. This
  is the single most valuable recovery path in the system, because `Ctrl-C` during an effect is a thing
  you will actually do.
- **Confirmations are not replayed on fork.** With `idempotencyScope: lineage`, a fork of a run that
  already opened a PR previews *"UPDATE existing PR #418"*, not *"create PR"*.
- **Headless mode requires an acknowledgement string, not a flag.** `kairos run --unattended` refuses
  unless config contains `unattended: { iUnderstandEffectsWillNotBeConfirmed: "yes-<hostname>" }`.
  Making the operator type it once is the point; a bare `--yes` that silently merges PRs is how this
  class of tool earns its reputation.

### Two things a single-user tool needs that a fleet did not

**Dry-run is cheaper and better here.** Because the effect set is CLI-shaped, a dry run prints the
literal commands. `kairos run --dry-run` performs nothing and appends `effect.simulated`;
`kairos run effects <run>` shows what has been applied and what compensation would unwind — *before*
you answer an approval. And **dry-run is the default for a workflow's first execution after publish**
unless `--live` is passed: a strong default, zero cost, and the cheapest possible guard against a
freshly written workflow doing something surprising with your credentials.

**An unattended-effect ceiling.** Triggers fire while you sleep, on a machine holding your `gh` token.
Cap it: `maxUnattendedPRs: 3`, `maxUnattendedPushes: 10`, zero pushes to protected refs, zero
`shell.exec` outside the allowlist — and block on a human task past the ceiling with the count in the
reason. This is the local analogue of a queue-depth limit, applied to the outside world instead of the
queue, and it is what prevents the "I woke up to 40 PRs" story.

---

## Gates that stop meaning anything

A gate whose approval rate is 100% over dozens of runs and whose median decision time is four seconds
should be **deleted, not celebrated.** You are both the operator and the workflow author here, so give
yourself the instrument:

```console
$ kairos gates report --since 30d
gate                  asked  approved  p50      evidence-dwell  verdict
push-approval            34        31   48s              11s    healthy
release-approval          9         9   2m10s            38s    healthy
lint-approval            61        61   3s                0s    ⚠ delete this gate
                                                                100% approved,
                                                                no evidence ever read
```

`kairos constraints stats` does the same for automated gates: fires per 100 runs, true-catch rate,
waiver rate, wall-clock cost. High fires plus high waivers means the constraint is wrong. **Never
fires means it is broken** — verify with a deliberately violating fixture via
`kairos constraints test <id> --fixture …`.
