package registry

import (
	"fmt"
	"time"

	"github.com/williamokano/kairos/internal/domain"
)

// agentActors are the built-in actor kinds 03-workflows.md calls "an agent
// actor" for retry-defaulting purposes (decision #5's neighbour: which
// actors get the write-node retry upgrade). "opencode" joins this set as
// of NL-29's harness-integration pass (L22-harness-integration.md): it is
// a real, live-verified agent CLI in this environment, wired the same as
// claude/codex/gemini everywhere this map (and llmActorKinds in
// internal/engine/admission.go, and the actor switch in
// internal/engine/dispatch.go) is consulted.
var agentActors = map[string]bool{"claude": true, "codex": true, "gemini": true, "opencode": true, "local": true}

// requiresOutputSchema reports whether actor's output must be
// author-declared (agent and shell actors — L8's typed-contract concern)
// versus optional-with-a-permissive-default (human and builtin.* actors,
// whose contract is defined by the actor implementation itself, not by
// free-form LLM JSON the corpus's typed-contracts law exists to guard).
func requiresOutputSchema(actor string) bool {
	if actor == "human" || actor == "noop" {
		return false
	}
	if len(actor) >= 8 && actor[:8] == "builtin." {
		return false
	}
	// "rule" is L05's placeholder actor (12-build-plan.md's milestone
	// spec only — see internal/engine/actor_rule.go's doc comment): it
	// always succeeds with a trivial in-process output and never
	// validates it against a declared schema, so requiring one here
	// would be a requirement with no effect. Not in 03-workflows.md's
	// actor enum at all.
	if actor == "rule" {
		return false
	}
	// "effect" is L12's builtin-effect actor (git.push, gh.pr.create):
	// its output is always {"externalRef": "..."} or {"simulated": true},
	// shaped by internal/engine's dispatchEffectActor, never by
	// free-form actor output — same posture as "rule".
	if actor == "effect" {
		return false
	}
	// "spawn" is L17's coordinator actor: its output is the join
	// summary internal/engine's dispatchSpawnChildren shapes, never
	// free-form author output — same posture as "rule"/"effect".
	if actor == "spawn" {
		return false
	}
	return true
}

// ApplyDefaults extracts every node's dynamic-shaped fields (output,
// inputs, on, wait, params) from doc's raw map, applies every default from
// 03-workflows.md's defaults table, compiles output/params schemas, and
// returns the fully-defaulted Definition. It does not run the publish
// validator — that is Validate's job, called separately by Load.
func ApplyDefaults(doc rawDoc) (Definition, error) {
	def := Definition{Name: doc.typed.Name}

	if paramsAny, ok := doc.raw["params"].(map[string]any); ok {
		schema, err := compileOutputShorthand(paramsAny)
		if err != nil {
			return Definition{}, fmt.Errorf("compiling params schema: %w", err)
		}
		def.ParamsSchema = schema
	}

	def.Limits = defaultLimits(doc.typed.Limits)

	gates, err := parseGates(doc.raw)
	if err != nil {
		return Definition{}, fmt.Errorf("parsing gates: %w", err)
	}
	def.Gates = gates

	nodeMaps := doc.nodeMaps()
	nodes := make([]NodeDef, 0, len(doc.typed.Nodes))
	for i, yn := range doc.typed.Nodes {
		var rawNode raw
		if i < len(nodeMaps) {
			rawNode = nodeMaps[i]
		}
		nd, err := defaultNode(yn, rawNode)
		if err != nil {
			return Definition{}, fmt.Errorf("node %q: %w", yn.ID, err)
		}
		nodes = append(nodes, nd)
	}
	def.Nodes = nodes

	return def, nil
}

func defaultLimits(yl *yamlLimits) LimitsDef {
	limits := LimitsDef{
		WallClock:         2 * time.Hour,
		MaxCostUSD:        10,
		MaxNodeExecutions: 100,
		MaxSpawnDepth:     2,
		LoopGuard:         LoopGuardDef{MaxIterationsPerNode: 3, OnExceeded: "escalate-to-human"},
	}
	if yl == nil {
		return limits
	}
	if yl.WallClock != "" {
		if d, err := time.ParseDuration(yl.WallClock); err == nil {
			limits.WallClock = d
		}
	}
	if yl.MaxCostUSD != 0 {
		limits.MaxCostUSD = yl.MaxCostUSD
	}
	if yl.MaxNodeExecutions != 0 {
		limits.MaxNodeExecutions = yl.MaxNodeExecutions
	}
	if yl.MaxSpawnDepth != 0 {
		limits.MaxSpawnDepth = yl.MaxSpawnDepth
	}
	if yl.LoopGuard != nil {
		if yl.LoopGuard.MaxIterationsPerNode != 0 {
			limits.LoopGuard.MaxIterationsPerNode = yl.LoopGuard.MaxIterationsPerNode
		}
		if yl.LoopGuard.OnExceeded != "" {
			limits.LoopGuard.OnExceeded = yl.LoopGuard.OnExceeded
		}
	}
	return limits
}

func defaultNode(yn yamlNode, rn raw) (NodeDef, error) {
	nd := NodeDef{
		ID:              domain.NodeID(yn.ID),
		Actor:           yn.Actor, // no default — decision #5: required per node
		Prompt:          yn.Prompt,
		Context:         yn.Context,
		Workspace:       WorkspaceMode(yn.Workspace),
		WorkspacePaths:  yn.WorkspacePaths,
		HostExclusive:   yn.HostExclusive,
		SessionAffinity: yn.SessionAffinity,
		Gates:           yn.Gates, // decision #6: no default library, empty when omitted
		Effects:         yn.Effects,
		With:            yn.With,
		Optional:        yn.Optional,
		SideEffectFree:  yn.SideEffectFree,
	}
	if nd.Workspace == "" {
		if yn.Actor == "spawn" {
			// 03-workflows.md: "a coordinator run declares workspace:
			// none, so it costs nothing at all" — the fan-out example
			// never types this field, so it is this document's default
			// for actor: spawn specifically, not a workflow-wide change
			// to decision #5's WorkspaceRead default (L17).
			nd.Workspace = WorkspaceNone
		} else {
			nd.Workspace = WorkspaceRead
		}
	}
	if nd.Gates == nil {
		nd.Gates = []string{}
	}
	if nd.Effects == nil {
		nd.Effects = []string{}
	}

	nd.RestartPolicy = RestartPolicy(yn.RestartPolicy)
	if nd.RestartPolicy == "" {
		if nd.SideEffectFree {
			nd.RestartPolicy = RestartRerun
		} else {
			nd.RestartPolicy = RestartFailToHuman
		}
	}

	timeout, err := parseDuration(yn.Timeout, 30*time.Minute)
	if err != nil {
		return NodeDef{}, err
	}
	nd.Timeout = timeout

	nd.Retry = defaultRetry(yn.Retry, nd.Workspace, nd.Actor)

	for _, a := range yn.Artifacts {
		nd.Artifacts = append(nd.Artifacts, ArtifactSpec(a))
	}

	if yn.Resources != nil && yn.Resources.Model != nil {
		nd.Resources = ResourcesDef{
			ModelClass: yn.Resources.Model.Class,
			ModelSlots: yn.Resources.Model.Slots,
			MaxCostUSD: yn.Resources.Model.MaxCostUSD,
		}
	}

	if yn.Spawn != nil {
		nd.Spawn = &SpawnDef{Workflow: yn.Spawn.Workflow, ForEach: yn.Spawn.ForEach, Strategy: yn.Spawn.Strategy, InheritWorkspace: yn.Spawn.InheritWorkspace}
	}
	if yn.Join != nil {
		nd.Join = &JoinDef{Mode: yn.Join.Mode, OnChildFailure: yn.Join.OnChildFailure}
		if nd.Join.OnChildFailure == "" {
			nd.Join.OnChildFailure = "fail"
		}
	}

	// Dynamic-shaped fields, from the raw map.
	if inputsAny, ok := rn["inputs"].(map[string]any); ok {
		nd.Inputs = make(map[string]InputRef, len(inputsAny))
		for k, v := range inputsAny {
			ref, err := parseInputRef(v)
			if err != nil {
				return NodeDef{}, fmt.Errorf("inputs.%s: %w", k, err)
			}
			nd.Inputs[k] = ref
		}
	}

	if onAny, ok := rn["on"].(map[string]any); ok {
		nd.On = map[domain.EdgeTrigger]domain.NodeID{}
		for k, v := range onAny {
			if s, ok := v.(string); ok {
				nd.On[domain.EdgeTrigger(k)] = domain.NodeID(s)
			}
		}
	}

	if err := defaultWait(&nd, rn); err != nil {
		return NodeDef{}, err
	}
	// 03-workflows.md's fan-out example declares no wait: block at all —
	// actor: spawn implies wait.on: [{kind: child-run}] the same way
	// SideEffectFree implies a RestartPolicy above; OnTimeout defaults to
	// park (a still-joining coordinator is not overdue for escalation,
	// just not done yet) since the doc gives no join timeout duration.
	if nd.Actor == "spawn" && nd.Wait == nil {
		nd.Wait = &WaitDef{On: []WaitSource{{Kind: WaitChildRun}}, OnTimeout: "park"}
	}

	if err := defaultOutputSchema(&nd, rn); err != nil {
		return NodeDef{}, err
	}

	return nd, nil
}

func defaultRetry(yr *yamlRetry, workspace WorkspaceMode, actor string) RetryDef {
	rd := RetryDef{MaxAttempts: 1}
	if workspace == WorkspaceWrite && agentActors[actor] {
		rd = RetryDef{MaxAttempts: 2, RetryOn: []string{"schema-invalid", "failure"}, FreshWorkspace: true}
	}
	if yr == nil {
		return rd
	}
	if yr.MaxAttempts != 0 {
		rd.MaxAttempts = yr.MaxAttempts
	}
	if len(yr.RetryOn) > 0 {
		rd.RetryOn = yr.RetryOn
	}
	if yr.FreshWorkspace {
		rd.FreshWorkspace = true
	}
	for _, m := range yr.Mutate {
		step := MutateStep{Attempt: m.Attempt, Actor: m.Actor}
		if m.Resources != nil && m.Resources.Model != nil {
			step.ModelClass = m.Resources.Model.Class
		}
		rd.Mutate = append(rd.Mutate, step)
	}
	return rd
}

func defaultWait(nd *NodeDef, rn raw) error {
	waitAny, ok := rn["wait"].(map[string]any)
	if !ok {
		return nil
	}
	wd := &WaitDef{}
	if onTimeout, ok := waitAny["onTimeout"].(string); ok {
		wd.OnTimeout = onTimeout
	}
	if weight, ok := waitAny["weight"].(string); ok {
		wd.Weight = weight
	}
	if timeoutStr, ok := waitAny["timeout"].(string); ok {
		d, err := time.ParseDuration(timeoutStr)
		if err != nil {
			return fmt.Errorf("wait.timeout: %w", err)
		}
		wd.Timeout = d
	}
	if onList, ok := waitAny["on"].([]any); ok {
		for _, entryAny := range onList {
			entry, ok := entryAny.(map[string]any)
			if !ok {
				continue
			}
			src := WaitSource{}
			if kind, ok := entry["kind"].(string); ok {
				src.Kind = WaitKind(kind)
			}
			if every, ok := entry["every"].(string); ok {
				if d, err := time.ParseDuration(every); err == nil {
					src.Every = d
				}
			}
			if until, ok := entry["until"].(string); ok {
				src.Until = until
			}
			if endpoint, ok := entry["endpoint"].(string); ok {
				src.Endpoint = endpoint
			}
			if match, ok := entry["match"].(string); ok {
				src.Match = match
			}
			if cmdAny, ok := entry["command"].([]any); ok {
				for _, c := range cmdAny {
					if s, ok := c.(string); ok {
						src.Command = append(src.Command, s)
					}
				}
			}
			wd.On = append(wd.On, src)
		}
	}
	if wd.Weight == "" {
		for _, src := range wd.On {
			if src.Kind == WaitHuman {
				wd.Weight = WeightRead
				break
			}
		}
	}
	nd.Wait = wd
	return nil
}

func defaultOutputSchema(nd *NodeDef, rn raw) error {
	if outAny, ok := rn["output"].(map[string]any); ok && len(outAny) > 0 {
		doc, err := shorthandToSchemaDoc(outAny)
		if err != nil {
			return fmt.Errorf("output: %w", err)
		}
		schema, raw, err := compileSchemaDocWithRaw(doc)
		if err != nil {
			return fmt.Errorf("output: %w", err)
		}
		nd.OutputSchema = schema
		nd.OutputSchemaRaw = raw
		return nil
	}
	if schemaAny, ok := rn["outputSchema"].(map[string]any); ok && len(schemaAny) > 0 {
		schema, raw, err := compileSchemaDocWithRaw(schemaAny)
		if err != nil {
			return fmt.Errorf("outputSchema: %w", err)
		}
		nd.OutputSchema = schema
		nd.OutputSchemaRaw = raw
		return nil
	}
	if !requiresOutputSchema(nd.Actor) {
		// The permissive default schema a node whose actor kind doesn't
		// require a declared output (human, builtin.*) gets when it omits
		// output/outputSchema — see the "outputSchema required" refinement
		// in L03-definition-validator.md's Documented decisions.
		schema, raw, err := compileSchemaDocWithRaw(map[string]any{"type": "object"})
		if err != nil {
			return err
		}
		nd.OutputSchema = schema
		nd.OutputSchemaRaw = raw
	}
	return nil
}
