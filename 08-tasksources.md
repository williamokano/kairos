# 08 — Triggers

How work arrives without you asking. Four ways in, **one code path out**: each produces a
`trigger.received` event and a Run. There is no "ad-hoc mode" that bypasses the log — that is the law
most at risk in this variant, because "a binary that just does the work for me" is exactly the pressure
that produces a fast path.

---

## The five entry points, ranked by how much you will actually use them

| | Mechanism |
| --- | --- |
| **1. `kairos do "…"` / the TUI composer** | Not a task source at all — a direct trigger. This is ~80% of use and it should never be routed through the polling machinery. |
| **2. The inbox** | `~/.kairos/inbox/*.md` with YAML front matter. Nearly free, and the best local affordance in the design. |
| **3. Pollers** | GitHub issues, Jira, Linear, on a local ticker. |
| **4. `cron`** | Nightly dependency updates, weekly flake sweeps. |
| **5. `repo-watch`** | Local-only and genuinely new: watch the repo, and when a test starts failing, start a fix run. Nothing in a fleet design has this, because a fleet has no idea what you just saved. |

Webhooks are deliberately not on that list. See below.

---

## The inbox

```markdown
---
flow: implement
project: orders
priority: 5
budget: 8.00
---
Add cursor pagination to GET /orders. Keep the existing offset params working
for one release and mark them deprecated in the OpenAPI spec.
```

`cp task.md ~/.kairos/inbox/` and it starts.

Mechanism: fsnotify with a 5s poll fallback (fsnotify is unreliable across editors' atomic-save dances
and over network mounts); a file is picked up once it has been unmodified for 2s; **pickup is
`rename(inbox/x.md, inbox/.taken/<runID>-x.md)` — the atomic rename *is* the dedupe**, in one syscall.
`dedupeKey = "inbox:" + sha256(content)`, so re-dropping identical content inside the window is a no-op
and the file lands in `.dup/` with a note. Failures move to `.failed/` with a sibling `.err.json`.

Why it earns its place: it composes with everything already on your machine. A Shortcut, a Raycast
script, an `at` job, a `git post-commit` hook, or a mail rule can all create work knowing nothing about
kairos beyond "write a file."

---

## Polling, and why not webhooks

```yaml
tasksources:
  - kind: github-issues
    repo: acme/orders
    filter: { labels: [kairos], state: open }
    flow: implement
    project: orders
    every: 2m
```

One goroutine per enabled source, jittered:

```text
next_poll_at   = now + interval ± rand(0, interval/4)
on error       : interval × 2^min(consecutiveErrors, 5), capped 30m; retryAfter overrides
on 429         : honour retryAfter exactly; mark the source `throttled` in kairos status
5 consecutive errors      → unhealthy, stop polling, surface the message
non-advancing cursor with items, 3 polls running → unhealthy
```

**120 seconds, not 30.** You share GitHub's rate limit with your own `gh`, so an aggressive poller means
*your* `git push` starts failing. The fleet design solved this with a rate pool and headroom; locally the
solution is a slower default plus **ETag conditional requests** — a `304` costs no quota at all. For
GitHub specifically, prefer `GET /notifications` with `If-None-Match` over repeated issue searches: one
cheap conditional request covering every repo you watch, which is what makes 120s feel instant.

### The laptop-sleep problem, which a fleet never had

A control plane down for six hours was an incident. A laptop closed for fourteen hours is Tuesday.

- **`cron` defaults to `catchUp: skip`, and that default must never change.** Waking to six nightly runs
  firing at once is exactly the surprise that gets a tool uninstalled.
- The poller detects a wall-clock jump (monotonic vs wall divergence > 2× interval) and treats the next
  cycle as a **cold start**: one poll per source at the stored cursor, no catch-up loop, and
  `source.resumed{gap}` in the log.
- Sources are paused for free while the process is suspended, and explicitly with `kairos src pause` —
  which is what you type before boarding a plane.

### State, all owned by the daemon

Never by the plugin: a plugin that keeps its own cursor cannot be restarted safely.

```sql
CREATE TABLE source (
  id TEXT PRIMARY KEY, kind TEXT NOT NULL, config TEXT NOT NULL,
  flow TEXT, project TEXT,
  interval_s INTEGER NOT NULL DEFAULT 120,
  enabled INTEGER NOT NULL DEFAULT 1,
  health TEXT NOT NULL DEFAULT 'unknown',      -- healthy | throttled | unhealthy
  health_reason TEXT, consecutive_errors INTEGER NOT NULL DEFAULT 0,
  last_poll_at TEXT, next_poll_at TEXT
) STRICT;

CREATE TABLE source_cursor (
  source_id TEXT PRIMARY KEY REFERENCES source(id) ON DELETE CASCADE,
  cursor TEXT, etag TEXT, updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE trigger_dedupe (
  dedupe_key TEXT PRIMARY KEY,
  source_id TEXT, item_id TEXT, run_id TEXT,
  created_at TEXT NOT NULL, expires_at TEXT NOT NULL     -- +30d
) STRICT;
```

Dedupe is one statement, and it is the difference between this working and not:

```sql
INSERT INTO trigger_dedupe(dedupe_key, source_id, item_id, run_id, created_at, expires_at)
VALUES (?,?,?,?,?,?) ON CONFLICT(dedupe_key) DO NOTHING RETURNING run_id;
```

Zero rows means already triggered: skip, and report the existing `run_id` upstream on `ack`. This is the
one place `ON CONFLICT DO NOTHING` is correct — on the `events` table it is **forbidden**, because
`UNIQUE(stream_id, sequence)` must *raise* so the engine's optimistic append fails loudly rather than
dropping a fact.

The dedupe key is keyed on the **triggering condition**, not the object:
`gh-<repo>-<number>-labeled-<label>-<updatedAt>`. So re-labelling an issue after a run completes triggers
a new run, while the same state polled twice does not.

### Webhooks: opt-in, bring your own tunnel

Inbound webhooks need a publicly reachable address and a laptop does not have one. Rejected: shipping an
embedded tunnel client (a vendored network daemon, an account, a rotating credential, and a support
surface — to save 118 seconds of latency for one user), and a kairos-operated relay (that is a hosted
service, and the premise is that there isn't one).

```console
$ kairos src add webhook --name gh-hook --flow implement --project orders
webhook  http://127.0.0.1:7717/v1/hook/gh-hook
secret   whsec_8f2a…

Kairos does not create tunnels. Expose that URL yourself, e.g.
  cloudflared tunnel --url http://127.0.0.1:7717
  tailscale funnel 7717
Until then this source is inert and `kairos status` will say so.
```

Enabling a webhook source is what causes the daemon to bind a TCP port *at all*; with none configured the
only listener is the unix socket, which removes the external attacker from the threat model entirely.
When enabled: HMAC verified **before parsing**, failures dropped with a counter and no body that leaks
whether the source exists, and the same `trigger_dedupe` table carries it — GitHub redelivers routinely,
so the acceptance test stands unchanged: *the same upstream event delivered twice by poll overlap **and**
webhook redelivery produces exactly one run.*

---

## The TaskSource contract

Three operations. `watch` folds into stream mode rather than being a fourth.

```json
op describe → {"name","kinds","ops","secrets":[…],"defaultInterval":"120s","configSchema":{…}}

op poll     → in  {"cursor": string|null, "limit": int}
              out {"items":[WorkItem], "cursor": string, "pollAfter": duration|null}

op ack      → in  {"itemID","dedupeKey","outcome":"triggered|succeeded|failed|rejected",
                   "runID","resultURL"?,"summary"?,"reason"?,"idempotencyKey"}
              out {}
```

```go
type WorkItem struct {
    ID        string            `json:"id"`         // stable upstream id
    DedupeKey string            `json:"dedupeKey"`  // REQUIRED; empty is a contract violation
    Title     string            `json:"title"`
    Body      string            `json:"body,omitempty"`     // NEW
    Project   string            `json:"project,omitempty"`  // NEW — which directory this is about
    Flow      string            `json:"flow,omitempty"`
    Params    json.RawMessage   `json:"params,omitempty"`
    Priority  int               `json:"priority,omitempty"`
    Budget    float64           `json:"budget,omitempty"`   // NEW
    Labels    map[string]string `json:"labels,omitempty"`
}
```

Three fields are new, and the first is *necessary*:

- **`Project`.** On a fleet, a project plus the flow's git spec determined where work happened and the
  workspace was provisioned on demand from a repo URL. Locally, work happens **in a directory that
  already exists on your disk**, so the item must name it. Resolution order: explicit `project` → a
  registered project whose remote matches `params.repo` → clone into `~/.kairos/repos/<owner>/<name>` and
  register it → **reject the item** with `reason: "no project for acme/orders; kairos src add"` and let
  `ack` say so upstream.
- **`Body`**, because locally a prose task is often the entire payload and forcing it through `params`
  JSON is friction for no gain.
- **`Budget`**, so an issue can carry its own cap.

`ack` routes **through the effect manager** with an idempotency key, not as a direct call — because a
duplicated "I've started work on this" comment is precisely the debris that effects exist to prevent.

---

## Plugins

**Primary: external executables speaking newline-delimited JSON on stdin/stdout, discovered in
`~/.kairos/plugins/`. Secondary and unapologetic: the shipped integrations are compiled in.**

| Mechanism | Verdict |
| --- | --- |
| Go plugins (`plugin.Open`) | **Rejected.** Requires an identical Go toolchain and identical versions of every shared dependency. On a laptop where the binary came from Homebrew and the plugin from `go build`, this is guaranteed to fail. |
| gRPC over a unix socket | **Rejected as the extension path.** It makes a 40-line GitHub poller cost a `.proto`, `protoc` plus two plugins installed, vendored stubs, a socket lifecycle, a health check, and a supervisor with backoff. That tax is worth paying to protect a shared control plane from a bad plugin; it is not worth paying so one person can add Linear support on a Saturday. |
| Compiled-in only | **Rejected as the only mechanism** (a third party would have to fork), **adopted for the builtins.** `github`, `jira`, `linear`, `inbox`, `cron`, `repo-watch`, `git`, `shell` ship inside the binary behind the same interface — no process, no IPC, no serialisation. Ninety percent of users never touch a plugin file. |
| **stdio NDJSON executables** | **Chosen for third parties.** Any language. `chmod +x` is the install. Testable with `echo '{…}' \| ./plugin \| jq`. |

**Discovery** reads `~/.kairos/plugins/<name>/plugin.json` **without executing anything** (startup stays
fast and a broken plugin cannot wedge boot); `describe` is called lazily on first use and cached against
the executable's mtime+size.

**Invocation, one-shot by default:** one process per call, request as a single JSON object on stdin, one
JSON object on stdout, exit 0. stderr is captured verbatim into the daemon log. No supervision, no health
checks, no restart policy — a crashed plugin is a failed call with a stack trace you can read.

```json
→ {"v":1,"op":"poll","callID":"c_01K4Z8","plugin":"github-issues",
   "config":{"repo":"acme/orders","label":"kairos"},
   "input":{"cursor":"2026-08-12T09:58:04Z","limit":50},
   "deadline":"2026-08-12T10:00:34Z"}

← {"v":1,"callID":"c_01K4Z8","ok":true,
   "output":{"items":[…],"cursor":"2026-08-12T10:00:12Z","pollAfter":"120s"}}

← {"v":1,"callID":"c_01K4Z8","ok":false,
   "error":{"code":"rate_limited","message":"secondary rate limit",
            "retryable":true,"retryAfter":"63s"}}
```

Error codes are a closed set: `bad_request`, `unauthorized`, `not_found`, `unsupported_op`,
`rate_limited`, `upstream`, `internal`. `retryable` + `retryAfter` drive backoff; everything else marks
the source unhealthy **with the message shown in `kairos status`**, never a bare socket error. Non-zero
exit with no JSON is normalised to `{"code":"internal","message":"<exit N>: <last 2KiB of stderr>"}`.

**Stream mode** (opt-in) keeps the process up with NDJSON both ways correlated by `callID`, for
webhook-fed sources and long-lived actors — which is where the original design's `Watch` RPC lands with
no gRPC.

**Secrets never appear in the request body.** They arrive as environment variables
(`KAIROS_SECRET_GITHUB_TOKEN` for a declared `github_token`), so the request can be recorded in the event
log as-is with no redaction pass. The engine refuses to start a plugin whose declared secrets are unset,
and records `secret.accessed{plugin, secret, callID}` per call.

### A whole TaskSource, in thirty lines

That it is this cheap is the argument for the contract.

```bash
#!/usr/bin/env bash
# ~/.kairos/plugins/gh-issues/gh-issues       chmod +x
set -euo pipefail
req=$(cat); op=$(jq -r .op <<<"$req"); cid=$(jq -r .callID <<<"$req")
repo=$(jq -r '.config.repo' <<<"$req")
ok(){ jq -cn --arg c "$cid" --argjson o "$1" '{v:1,callID:$c,ok:true,output:$o}'; }

case "$op" in
describe)
  ok '{"name":"gh-issues","kinds":["tasksource"],"ops":["describe","poll","ack"],
       "secrets":["github_token"],"defaultInterval":"120s",
       "configSchema":{"type":"object","required":["repo"],
         "properties":{"repo":{"type":"string"},"label":{"type":"string","default":"kairos"},
                       "project":{"type":"string"}}}}' ;;
poll)
  since=$(jq -r '.input.cursor // "1970-01-01T00:00:00Z"' <<<"$req")
  label=$(jq -r '.config.label // "kairos"' <<<"$req")
  proj=$(jq -r '.config.project // ""' <<<"$req")
  items=$(GH_TOKEN="$KAIROS_SECRET_GITHUB_TOKEN" gh api -X GET \
      "repos/$repo/issues" -f labels="$label" -f state=open -f sort=updated -f since="$since" \
    | jq -c --arg r "$repo" --arg l "$label" --arg p "$proj" '[ .[] | {
        id:        "gh:\($r)#\(.number)",
        dedupeKey: "gh-\($r)-\(.number)-labeled-\($l)-\(.updated_at)",
        title:     .title,
        body:      (.body // ""),
        project:   (if $p == "" then null else $p end),
        params:    {repo:$r, issue:.number, url:.html_url},
        priority:  (if (.labels|map(.name)|index("urgent")) then 10 else 0 end),
        labels:    {updatedAt:.updated_at}
      } ]')
  next=$(jq -r 'map(.labels.updatedAt)|max // empty' <<<"$items")
  ok "$(jq -cn --argjson i "$items" --arg c "${next:-$since}" \
        '{items:$i,cursor:$c,pollAfter:"120s"}')" ;;
ack)
  n=$(jq -r '.input.params.issue' <<<"$req"); out=$(jq -r '.input.outcome' <<<"$req")
  url=$(jq -r '.input.resultURL // "—"' <<<"$req"); key=$(jq -r '.input.idempotencyKey' <<<"$req")
  GH_TOKEN="$KAIROS_SECRET_GITHUB_TOKEN" gh api -X POST "repos/$repo/issues/$n/comments" \
    -f body="kairos: $out → $url
<!-- kairos-ack:$key -->" >/dev/null
  ok '{}' ;;
*) jq -cn --arg c "$cid" --arg o "$op" \
     '{v:1,callID:$c,ok:false,error:{code:"unsupported_op",message:$o}}' ;;
esac
```

```console
$ kairos plugin add ~/src/gh-issues && kairos plugin test gh-issues
✓ describe          kinds=[tasksource] ops=[describe poll ack]
✓ dedupe stable     3 polls, 12 items, 4 distinct keys
✓ cursor monotonic  1970-01-01 → 2026-08-12T09:58:04Z → 2026-08-12T10:00:12Z
✓ replay clean      re-poll at the final cursor yields 0 items
✓ malformed input   typed error, no panic
```

Note the `<!-- kairos-ack:<key> -->` marker: it is what makes `probe` implementable against a system with
no idempotency support, and the reason `ack` goes through the effect manager instead of being called
directly.

**Conformance is kept from the original design, because it is the valuable part.** `kairos plugin test`
asserts: dedupe keys stable across repeated polls; cursors strictly advance; a replayed poll yields zero
new items; `apply` twice with one key returns `alreadyApplied: true` with an identical ref; `compensate`
is idempotent and succeeds when never applied; `probe` reports both states correctly; malformed upstream
data returns a typed error rather than a panic; an unknown op returns `unsupported_op`.

### Effects, same contract

```json
→ {"v":1,"op":"apply","plugin":"github","config":{"repo":"acme/orders"},
   "input":{"action":"github.create_pr","idempotencyKey":"run_01K4Z:push:1",
            "args":{"head":"kairos/run_01K4Z","base":"main","title":"…","body":"…"}}}

← {"v":1,"ok":true,
   "output":{"externalRef":{"prNumber":418,"url":"https://github.com/acme/orders/pull/418"},
             "alreadyApplied":false,"compensation":"github.close_pr"}}
```

The implementation detail that matters: **`apply` probes first.** The crash-between-send-and-receive case
is the *normal* case on a laptop that sleeps mid-request, so an `apply` that does not check for its own
marker before acting will open a second PR the first time you close the lid at the wrong moment.

Every action must declare a compensation in `describe`, and a workflow using an action with no declared
compensation fails `kairos check`. Every plugin re-checks the action against policy even though the engine
already did — an actor can attempt an action it never declared.

---

## Admission, and rejecting rather than dropping

A run created by a trigger goes through the same admission check as one you typed, and it can be
**rejected**:

```text
queued >= maxQueued (40)  → REJECT, and report the rejection upstream via `ack`
```

Explicit rejection matters: a misconfigured integration firing 500 events must produce 500 *visible*
rejections that get reported back to the source, because silent truncation reads as "the system ignored
me."

The same applies to the attention budget. `maxOpenDecisions: 5` stops *starting* work when five things
already wait on you — backpressure on the scarcest resource in the system, which on one machine has
exactly one member.
