# L03 — Definition + Validator

## Depends on

L02 (event store), transitively L01 (domain model). Nothing here calls `internal/eventstore` —
L03 has no consumer yet, proven by tests alone, the same pattern L01 and L02 used.

## Scope

**In.**
- `internal/registry`: `Definition`/`NodeDef` — the rich, fully-authored workflow shape — and
  `ProjectGraph`, the seam projecting only the routing-relevant subset into `domain.Graph`.
- YAML parsing (`sigs.k8s.io/yaml`, first use of this dependency) into an intermediate
  `rawDoc` (typed skeleton + a raw `map[string]any` for dynamically-shaped fields and the
  denylist walk).
- Defaulting (`ApplyDefaults`) per `03-workflows.md`'s defaults table: workspace, timeout,
  retry (including the write+agent-actor upgrade), limits, loopGuard.
- The `output:`/`params:` shorthand compiler (`compileOutputShorthand`) — a pragmatic reading
  of the shorthand grammar into draft-2020-12 JSON Schema, reusing the same
  `NewCompiler+AddResource+Compile` sequence `internal/events/registry.go` established, as its
  own leaf function (no shared `util` package).
- The publish validator (`Validate`): the deleted-fields denylist, the `network.egress`/
  `network.allow` special case, output-schema-required (for agent/shell actors), onTimeout-
  required-when-wait-present, node ID rules, and the static input-reference subset.
- Document-order edge defaulting (`resolveEdges`) producing a fully-resolved
  `map[domain.NodeID]map[domain.EdgeTrigger]domain.NodeID` for every trigger
  `domain.Advance` can observe, including `denied` (see decision #1).
- `Load`/`LoadBytes`: the one entrypoint later layers call.
- Fixtures: the README's `fix-issue.yaml` (verbatim, quoting fixed per the YAML `on:` gotcha —
  see decision #9), `ci-watch.yaml` and `human-approval.yaml` (the wait examples), and
  `testdata/invalid/*.yaml` for the validator's rejection paths.

**Out.**
- Gate evaluation (L10) — `Gates []string` stays opaque.
- Actor invocation (L08), effects execution (L12).
- Domain profiles (`internal/registry/domains.go`, `13-domains.md`, phase 1) — not created here,
  even though it will live in this same package later.
- Spawn/join dispatch (L17) — `Spawn`/`Join` are parsed and structurally validated only.
- Gate-library toolchain detection (`go.mod` → build/lint/test commands) — L10's concern.
- Runtime input-*value* validation against a prior node's actual output — decision #2.
- `kairos check-output`/any CLI surface — L04+.

## Documented decisions

1. **`denied → $fail` is defaulted for every node now**, even though nothing through L02 can
   yet produce a denied outcome. `domain.Advance`'s `ErrUnresolvedEdge` is a hard error, not a
   graceful skip — leaving it unresolved would break every already-published workflow the
   moment L11/L12 starts emitting it. Same fallback as `failure`/`timeout`.
2. **The static input-reference validator subset** only checks that `$.outputs.<nodeID>...`
   paths name a node earlier in document order. Whether that node's output schema actually
   contains the referenced field, and `$.params.*`/`$.artifacts.*` references, are out of
   scope — cross-referencing JSONPath against arbitrary (possibly `$ref`'d) schemas is a harder
   problem with little payoff for a single-author tool.
3. **`Wait.TimeoutAt` stays nil from `ProjectGraph`.** A workflow's `wait.timeout` is a
   *relative* duration; resolving it to an absolute instant needs "now," which parsing must not
   read (AGENTS §4 rule 4). The relative duration lives on `NodeDef.Wait.Timeout` for the
   engine (L05/L06) to add to dispatch-time "now" when it issues `CmdArmTimer`.
4. **Multiple `wait.on` sources collapse to one `domain.WaitKind`.** L01's `WaitSpec` has one
   `Kind`; the first `on` entry's kind wins for the projected `domain.Node`, and the full list
   stays on `NodeDef.Wait.On` for the engine to act on.
5. **`actor:` has no default — it's required per node.** `03-workflows.md` never states a bare
   default; every example sets it explicitly.
6. **`gates:` defaults to an empty slice, not the detected-toolchain library.** Toolchain
   detection is L10's gate-library concern; L03 only carries through what's authored.
7. **Deleted fields are rejected via an explicit denylist walk** over the raw
   `map[string]any`, not `encoding/json`'s `DisallowUnknownFields` — gives a precise, named
   error per field (`"node \"n1\": field \"resources.cpu\" was removed..."`) instead of
   rejecting genuinely-new-but-harmless keys with a generic message.
8. **`outputSchema` is required only for agent and shell actors, not `human`/`builtin.*`/`noop`.**
   `03-workflows.md` states "Stays required. Non-negotiable" in its defaults table, but its own
   `fix-issue.yaml` example omits `output:` on the `approve` (human) and `pr` (builtin.gh-pr)
   nodes. This is a real tension in the source doc. Resolution: L8 ("typed contracts... the
   most valuable law... with isolation gone, typed contracts plus gates carry the whole safety
   budget") is fundamentally about validating free-form LLM JSON output, which human and
   builtin-actor nodes don't produce — their contract is defined by the actor implementation,
   not by parsing an agent's text. `human`/`noop`/`builtin.*` actors get a permissive
   `{"type":"object"}` default when `output`/`outputSchema` is omitted; agent (`claude`,
   `codex`, `gemini`, `local`) and `shell` actors still hard-require one. This is a refinement
   discovered while building the canonical fixture, not a silent narrowing — the fixture would
   not parse under the original stricter reading.
9. **Bare `on:` YAML keys are a real gotcha, worth registering.** `sigs.k8s.io/yaml` (via
   go-yaml's YAML 1.1 boolean resolution) decodes an unquoted `on:` key as the boolean `true`,
   silently discarding the intended key name. Every fixture using `wait.on` or a node-level edge
   override quotes it as `"on":`. This is not a bug in this document's parser — it is inherent
   to the YAML 1.1 spec `sigs.k8s.io/yaml` implements — but it is exactly the kind of trap
   AGENTS §8 requires registering, so it is noted here and should move to `11-limitations.md`
   in the document that first ships user-facing YAML authoring UX (L04+).

## Public interfaces

```go
type Definition struct { Name, SourcePath string; ParamsSchema *jsonschema.Schema
    Nodes []NodeDef; Limits LimitsDef }
type NodeDef struct { ID domain.NodeID; Actor, Prompt string
    Inputs map[string]InputRef; OutputSchema *jsonschema.Schema
    Context []string; Workspace WorkspaceMode; WorkspacePaths []string; HostExclusive bool
    Resources ResourcesDef; Timeout time.Duration; SessionAffinity string
    Retry RetryDef; Gates, Effects []string; Artifacts []ArtifactSpec
    Wait *WaitDef; Spawn *SpawnDef; Join *JoinDef; Optional bool
    On map[domain.EdgeTrigger]domain.NodeID }

func Load(path string) (Definition, error)
func LoadBytes(data []byte, sourcePath string) (Definition, error)
func ProjectGraph(def Definition) (domain.Graph, error)
```

## Files to create

```
internal/registry/doc.go
internal/registry/definition.go
internal/registry/parse.go
internal/registry/defaults.go
internal/registry/validate.go
internal/registry/schema.go
internal/registry/edges.go
internal/registry/project.go
internal/registry/load.go
internal/registry/schema_test.go
internal/registry/parse_test.go
internal/registry/edges_test.go
internal/registry/project_test.go
internal/registry/testdata/fix-issue.yaml
internal/registry/testdata/ci-watch.yaml
internal/registry/testdata/human-approval.yaml
internal/registry/testdata/invalid/network-egress.yaml
internal/registry/testdata/invalid/deleted-worker.yaml
internal/registry/testdata/invalid/deleted-resources-cpu.yaml
internal/registry/testdata/invalid/missing-output-schema.yaml
internal/registry/testdata/invalid/missing-on-timeout.yaml
```

`go.mod`: add `sigs.k8s.io/yaml`.

## Data changes

None. `internal/registry` has no I/O beyond reading a definition file (`os.ReadFile` in `Load`)
and touches no database.

## Acceptance criteria

- `go build ./...`, `go vet ./...`, `golangci-lint run` clean; `go test ./... -race` green (17
  new tests in `internal/registry`, 71 total across the repo).
- `TestProjectGraph_fixIssueMatchesHandRolledGraph` parses the real README `fix-issue.yaml`,
  projects it with **no hand-authored `domain.Graph`**, and drives it through
  `domain.Advance` to `RunSucceeded` — the real end-to-end proof this layer works.
- `TestProjectGraph_ciWatchWaitNodeEntersWaitingDirectly` proves a wait node is dispatched
  straight to `Waiting` via `CmdEnterWait`, matching L01's documented dispatch design.
- The denylist, `network.egress`, missing-output-schema, and missing-onTimeout rejections all
  fire with named, specific error messages, each covered by its own test.
- All nine architecture tests still pass with `internal/registry` in the tree (verified with a
  clean test-cache run, not a stale cached pass).
- `make cross` still builds for darwin/linux × arm64/amd64.
- No `TODO`, `FIXME`, or commented-out code in the diff.

## Tests

- `schema_test.go`: the shorthand grammar — required/optional scalars, arrays, nested objects,
  unknown-type rejection.
- `parse_test.go`: `fix-issue.yaml`/`ci-watch.yaml`/`human-approval.yaml` field-by-field
  assertions after defaulting; the five invalid fixtures' rejections; `LoadBytes` parity.
- `edges_test.go`: document-order defaulting including the rejected-self-loop and denied-default,
  and author-override precedence.
- `project_test.go`: the two end-to-end `domain.Advance`-driven proofs described above.

## Benchmarks

None. Parsing a workflow definition is a one-time, off-the-hot-path operation (once per
publish, not once per run); nothing here approaches L02's append-latency sensitivity.

## Migration

None. No prior schema.

## Future work

- L04 (daemon: API + SSE + CLI) is where `kairos run <file>`/`kairos check` actually wire
  `registry.Load` into a CLI verb, and where the `on:` YAML-boolean gotcha (decision #9) should
  gain user-facing documentation or a friendlier parse-time error (detecting a boolean value
  where a trigger-name map was expected and suggesting the quoting fix).
- L05 (engine) is `Definition`'s first real consumer beyond tests: it looks up a `Definition` by
  `TriggerReceived.DefinitionRef` whenever a `CmdStartNode` needs actor/prompt/inputs/gates —
  the whole reason that field already existed on L01's event.
- L08 (actor SDK) is what actually resolves `NodeDef.Actor` against a real CLI invocation and
  validates real agent output against `NodeDef.OutputSchema`.
- L10 (constraints + gates) resolves `NodeDef.Gates` against a real gate library, including the
  toolchain-detected specialization (`go.mod` → `go build`/`golangci-lint`/`go test`) this
  document deliberately does not implement (decision #6).
- L11/L12 (policy, effects) is where a `denied` outcome first becomes producible — decision #1's
  pre-resolved `denied → $fail` edge means no already-published workflow breaks when that ships.
- L17 (child runs) implements spawn/join dispatch against the `SpawnDef`/`JoinDef` this document
  only parses and structurally validates.
