# Kairos CLI guide and demo

A practical walkthrough of building `kairos`, starting the daemon, writing a workflow, and
running it end to end. Everything here uses only `actor: rule` and `actor: shell` nodes, so it
runs with no external LLM CLI, no GitHub token, and no network access — a good first demo before
wiring in `claude`/`codex`/`gh`.

## 1. Build

```sh
cd /path/to/kairos
go build -o ./bin/kairos ./cmd/kairos
export PATH="$PWD/bin:$PATH"
kairos version
```

`CGO_ENABLED=0` cross-compilation also works, if you need a binary for another platform:

```sh
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o ./bin/kairos-darwin-arm64 ./cmd/kairos
```

## 2. Where Kairos keeps its state

Everything lives under `$KAIROS_HOME` (default `~/.kairos`, or `$XDG_STATE_HOME/kairos` if that's
set). For this demo, use a throwaway directory so you don't touch your real state:

```sh
export KAIROS_HOME=/tmp/kairos-demo
mkdir -p "$KAIROS_HOME"
```

The daemon auto-starts the first time any command needs it — you never have to run `kairos serve`
yourself. It listens on a unix socket at `$KAIROS_HOME/daemon.sock` (mode `0600`), so nothing is
reachable off-host.

## 3. Command reference

| Command | What it does |
|---|---|
| `kairos version` | print the build version |
| `kairos status` / bare `kairos` | is the daemon up, uptime, active run count |
| `kairos run <file.yaml> [--param k=v ...] [--unattended]` | publish + start a run from a workflow file |
| `kairos ls [--status <s>] [-q]` | list runs |
| `kairos show <runID>` | a run's folded state — status, every node execution, attempt/iteration |
| `kairos conversation show <runID>` | a run's message thread (for `wait: conversation` nodes) |
| `kairos conversation send <runID> <text>` | post a message, resolving a `wait: conversation` node |
| `kairos approve <runID> --node <id> --confirm <decision> --reason <text> [--typed-confirm <word>]` | answer a `wait: human` node — deliberately has no `--yes`/`--all`, every decision needs a reason |
| `kairos cancel <runID> --reason <text>` | stop a run, signalling every in-flight node execution and compensating every applied effect — no `--compensate` toggle, it always compensates |
| `kairos diff <runID> [nodeID]` | a run's (or one node's) file-level change against its workspace, as a real unified diff |
| `kairos cost` | today's admission-time spend estimate against the configured daily cap (an ESTIMATE, never reconciled against real spend) |
| `kairos doctor` | host preflight: what the daemon can/can't run (`git`, `gh` on `PATH`, etc.) |
| `kairos db verify` | replay every event stream and diff it against the persisted projections |
| `kairos db reindex` | force every projection to rebuild from the log |
| `kairos check-output` | (used by actors, not you) validates `$KAIROS_OUTPUT` against `$KAIROS_SCHEMA` |

Every command accepts `-o json` for machine-readable output instead of the default table/text
format:

```sh
kairos -o json ls
```

## 4. Write a workflow

Workflows are YAML. A node's `actor` decides what runs it: `rule` (an instant, in-process
no-op — useful for demo/glue nodes), `shell` (`/bin/sh -c <prompt>`), or `claude`/`codex`/`gemini`/
`local` (a real LLM CLI, see §7). Every node declares its `output` shape; `shell`/LLM actors must
write valid JSON matching it to `$KAIROS_OUTPUT_PATH` (or `$KAIROS_OUTPUT` for the LLM file
contract) to succeed.

Save this as `demo.yaml`:

```yaml
name: demo

nodes:
  - id: start
    actor: rule
    output: { x: "string" }

  - id: greet
    actor: shell
    prompt: |
      echo "{\"greeting\": \"hello from node greet, run at $(date -u +%FT%TZ)\"}" > "$KAIROS_OUTPUT_PATH"
    output: { greeting: "string!" }

  - id: finish
    actor: rule
    output: { x: "string" }
```

This is the same shape as `cmd/kairos/testdata/milestone.yaml` in the repo, which the flagship
kill/restart integration tests drive — worth reading for a slightly richer example (retries,
idempotent shell nodes, a `sideEffectFree` node).

## 5. Run it

```sh
kairos run demo.yaml
# 01H...        running

kairos ls
# RUN ID          STATUS      STARTED               UPDATED
# 01H...          succeeded   2026-08-20T13:00:00Z  2026-08-20T13:00:01Z

kairos show 01H...
# run:    01H...
# status: succeeded
# nodes:
#   NODE      EXEC ID          STATUS      ATTEMPT  ITERATION
#   start     start#a1.i1      succeeded   1        1
#   greet     greet#a1.i1      succeeded   1        1
#   finish    finish#a1.i1     succeeded   1        1
```

Watch it live instead of polling `ls`/`show`, via `kairos logs --follow` — a client of the same
`GET /events` SSE stream, reconnecting and resuming on its own if the connection drops:

```sh
kairos logs 01H... --follow
# 1  01H...  run.started            {...}
# 2  01H...  node.execution.started {"NodeID":"start",...}
# 3  01H...  node.output.received   {"NodeID":"start",...}
# ...
```

Ctrl-C ends the tail cleanly. The bare, un-followed form (`kairos logs 01H...`) prints the same
history and exits — useful for a quick look without staying attached. Reading the SSE stream
directly still works too, and is exactly what `kairos logs` does under the hood:

```sh
curl -s --unix-socket "$KAIROS_HOME/daemon.sock" \
  "http://kairos/events?stream=<runID>&after=0"
```

Confirm the event log and the projected state agree (this is real, not cosmetic — it replays the
whole log and diffs it against the durable projections):

```sh
kairos db verify
# clean
```

Cancel a still-running run instead of waiting it out — this signals every in-flight node
execution and compensates every applied effect (there is no `--compensate` toggle: cancellation
always compensates):

```sh
kairos cancel 01H... --reason "no longer needed"
# cancelled

kairos show 01H...
# status: cancelled
# nodes:
#   NODE  EXEC ID    STATUS       ATTEMPT  ITERATION
#   nap   nap#a1.i1  interrupted  1        1
```

For a `workspace: write` node (§7 below wires one up for real), `kairos diff` shows exactly what
it changed — a real `git diff`, not a description of one:

```sh
kairos diff 01H...
# 846b188..242e3f2
#   list.go +1 -1
# ---
# diff --git a/list.go b/list.go
# index b7f937b..225a826 100644
# --- a/list.go
# +++ b/list.go
# @@ -1,5 +1,5 @@
#  package orders
#
#  func List() int {
# -	return 1
# +	return 2
#  }

kairos diff 01H... <nodeID>   # scope the diff to one node's own before/after
```

`kairos cost` reads back today's admission-time spend estimate against the configured daily cap
— an ESTIMATE (NL-30), never reconciled against what a run actually cost:

```sh
kairos cost
# 2026-08-21: no admitted request has recorded a cost estimate yet (cap $25.00)
# note: this is an admission-time ESTIMATE, never reconciled against real spend (NL-30)
```

## 6. A workflow with a human decision

`wait: human` nodes park until someone answers with `kairos approve` — there is deliberately no
`--yes`/`--all` flag, and every decision requires `--reason`:

```yaml
name: demo-approval

nodes:
  - id: propose
    actor: shell
    prompt: echo '{"summary":"delete the staging bucket"}' > "$KAIROS_OUTPUT_PATH"
    output: { summary: "string!" }

  - id: approve
    actor: human
    workspace: none
    inputs:
      summary: "$.outputs.propose.summary"
    output: { decision: "string!", reason: "string" }
    wait:
      "on":
        - kind: human
      weight: read
      timeout: 1h
      onTimeout: park
```

(`"on"` must be quoted — bare `on:` parses as the boolean `true` in YAML 1.1, a real gotcha this
codebase works around everywhere.)

```sh
kairos run demo-approval.yaml
# 01H...   running

kairos show 01H...
# NODE     EXEC ID        STATUS     ATTEMPT  ITERATION
# approve  approve#a1.i1  waiting    1        1
# propose  propose#a1.i1  succeeded  1        1

kairos approve 01H... --node approve --confirm approve --reason "reviewed, staging is empty"
# answered

kairos show 01H...
# status: succeeded
```

A `wait.weight: type` node instead requires `--typed-confirm <the node id, typed out>` — the
highest-friction tier, for the most irreversible decisions.

## 7. Wiring in a real LLM actor (optional)

`actor: claude`/`codex`/`gemini`/`local` nodes shell out to a real CLI. This needs two config
values set (env vars, or the daemon's config file):

```sh
export KAIROS_LLM_BINARY=/usr/local/bin/claude   # whichever CLI you're driving
export KAIROS_WORKSPACE_REPO=git@github.com:you/your-repo.git
```

`WorkspaceRepo` is required for any `workspace: write` node — Kairos clones it via `git clone
--reference <mirror>` into a private per-run workspace (never a worktree — see
`adr/0005-reference-clone-per-run.md`), so the actor gets a real, isolated git checkout to work
in. Nodes without `workspace: write` don't need it.

`internal/registry/testdata/fix-issue.yaml` in the repo is a realistic example: a `claude` node
plans work, a second `claude` node with `workspace: write` implements it behind gates
(`build`/`lint`/`no-todos`/`no-secrets`), a `human` node approves, and a `builtin.gh-pr` effect
node opens the PR. Don't run it against a real repo without reading it first — it opens a real
pull request.

## 8. Killing the daemon mid-run (what makes this durable)

This is the property the whole design exists to prove, so it's worth seeing once:

```sh
kairos run demo.yaml    # note the runID and that it's running
pkill -9 -f "kairos serve"

# the daemon is dead — nothing is watching the run right now
kairos status            # auto-starts a fresh daemon
kairos show <runID>      # reconciliation already ran before this answered:
                          # any node that was mid-execution when the daemon died
                          # is reaped, marked node.execution.lost, and retried
                          # (or parked, per its restart policy) — automatically
kairos db verify         # still clean
```

`cmd/kairos/kill_mid_run_test.go`'s `TestEngine_survivesKillMidRun` is the automated version of
exactly this scenario, asserted event-by-event rather than eyeballed.

## 9. Troubleshooting

- **`daemon not reachable`**: the auto-start didn't reach readiness in time (5s). Run `kairos
  status` again, or check `$KAIROS_HOME/daemon.lock` for a stale PID from a crashed daemon that
  didn't clean up (rare — reconciliation handles this, but if it looks wedged, `rm
  $KAIROS_HOME/daemon.lock $KAIROS_HOME/daemon.sock` and retry).
- **`a daemon is already running (pid ...)`**: something is already using this `$KAIROS_HOME`.
  Use a different `$KAIROS_HOME` for a second, independent demo instance.
- **A `shell` node fails validation**: it must write JSON matching its declared `output:` schema
  to `$KAIROS_OUTPUT_PATH` and exit 0. Check `$KAIROS_HOME/work/<runID>/<execID>/stdout.log` and
  `stderr.log` for what actually happened.
- **`kairos db verify` reports a mismatch**: this should never happen; it's the strongest
  correctness signal the system has. If you hit it, please treat it as a real bug report.
