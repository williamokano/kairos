package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/registry"
)

// SpawnChildRequest is everything a spawn: node's dispatch needs to bring
// one child Run into existence — internal/engine's own copy of
// internal/tasksource.CreateRunRequest's shape, kept deliberately
// separate rather than imported (see RunSpawner's doc comment).
type SpawnChildRequest struct {
	DefinitionRef string
	Params        json.RawMessage
	TriggerRef    string
	ParentRunID   string
}

// RunSpawner is the only way a spawn: node's dispatch can cause a new Run
// to exist. See engine.Config.Spawner's doc comment for why this is an
// injected interface rather than internal/engine importing
// internal/tasksource directly.
type RunSpawner interface {
	SpawnChild(ctx context.Context, req SpawnChildRequest) (runID string, err error)
}

// spawnTriggerPrefix is the L16 TriggerRef convention this document adds:
// "spawn:<parentRunID>:<nodeID>:<execID>:<index>" — everything
// handleChildRunFinished needs to route a child's terminal transition
// back to its coordinator, without internal/engine needing a database
// table to remember the relationship (the child's own first event
// carries it).
const spawnTriggerPrefix = "spawn:"

func formatSpawnTriggerRef(parentRunID, nodeID, execID string, index int) string {
	return fmt.Sprintf("%s%s:%s:%s:%d", spawnTriggerPrefix, parentRunID, nodeID, execID, index)
}

func parseSpawnTriggerRef(ref string) (parentRunID, nodeID, execID string, index int, ok bool) {
	if !strings.HasPrefix(ref, spawnTriggerPrefix) {
		return "", "", "", 0, false
	}
	parts := strings.SplitN(strings.TrimPrefix(ref, spawnTriggerPrefix), ":", 4)
	if len(parts) != 4 {
		return "", "", "", 0, false
	}
	idx, err := strconv.Atoi(parts[3])
	if err != nil {
		return "", "", "", 0, false
	}
	return parts[0], parts[1], parts[2], idx, true
}

var boundedStrategyPattern = regexp.MustCompile(`^bounded\((\d+)\)$`)

// parseBoundedStrategy extracts N from a validated "bounded(N)" string —
// registry.Validate already rejected anything else at publish time, so a
// parse failure here means a NodeDef reached dispatch without having been
// validated, a programmer error rather than a runtime condition.
func parseBoundedStrategy(s string) (int, error) {
	m := boundedStrategyPattern.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("engine: spawn.strategy %q is not bounded(N)", s)
	}
	n, _ := strconv.Atoi(m[1])
	return n, nil
}

// dispatchSpawnChildren makes CmdSpawnChildren real (L17): resolve the
// node's spawn: block, plan its children once (idempotent — a re-dispatch
// after a crash reuses the plan already on the log rather than
// re-resolving forEach, which could see different data if the referenced
// node's output somehow changed), then spawn the first bounded(N) batch.
//
// dispatchCmds follows resolveConversationWait/recoverLost's precedent:
// false from the live dispatch path (dispatch.go's CmdSpawnChildren case
// — the shard already watching this run's stream picks up and dispatches
// anything a failure append produces); true when reconcileSpawnJoin
// re-enters this function for an ExecWaiting exec that crashed before
// ChildRunsPlanned was ever recorded — Reconcile runs before any shard
// exists, so nothing else will ever act on those cmds unless this call
// does.
func (e *Engine) dispatchSpawnChildren(ctx context.Context, definitionRef string, c domain.CmdSpawnChildren, dispatchCmds bool) error {
	e.spawnMu.Lock()
	defer e.spawnMu.Unlock()
	return e.dispatchSpawnChildrenLocked(ctx, definitionRef, c, dispatchCmds)
}

// dispatchSpawnChildrenLocked is dispatchSpawnChildren's body, callable
// both from the locking entry point above and from
// reconcileSpawnJoinLocked's plan == nil re-entry — both already hold
// spawnMu by the time either calls this, so it must never lock itself
// (that would deadlock).
func (e *Engine) dispatchSpawnChildrenLocked(ctx context.Context, definitionRef string, c domain.CmdSpawnChildren, dispatchCmds bool) error {
	def, err := e.loadDefinition(definitionRef)
	if err != nil {
		return fmt.Errorf("loading definition: %w", err)
	}
	nd, ok := findNode(def, c.NodeID)
	if !ok || nd.Spawn == nil {
		return fmt.Errorf("engine: node %q has no spawn: block", c.NodeID)
	}
	if e.spawner == nil {
		return e.failSpawnNode(ctx, definitionRef, c.RunID, c.NodeID, c.ExecID,
			"no RunSpawner configured — spawn: nodes cannot run", dispatchCmds)
	}

	plan, err := e.spawnPlan(ctx, c.RunID, c.NodeID, c.ExecID)
	if err != nil {
		return fmt.Errorf("reading spawn plan: %w", err)
	}
	if plan == nil {
		items, err := e.resolveForEach(ctx, c.RunID, nd.Spawn.ForEach)
		if err != nil {
			return e.failSpawnNode(ctx, definitionRef, c.RunID, c.NodeID, c.ExecID,
				fmt.Sprintf("resolving spawn.forEach: %v", err), dispatchCmds)
		}
		depth, err := e.spawnDepth(ctx, c.RunID)
		if err != nil {
			return fmt.Errorf("resolving spawn depth: %w", err)
		}
		if depth+1 > def.Limits.MaxSpawnDepth {
			return e.failSpawnNode(ctx, definitionRef, c.RunID, c.NodeID, c.ExecID,
				fmt.Sprintf("spawn would exceed maxSpawnDepth (%d)", def.Limits.MaxSpawnDepth), dispatchCmds)
		}
		planned := make([]domain.ChildPlanItem, len(items))
		for i, item := range items {
			params, err := json.Marshal(map[string]json.RawMessage{"item": item})
			if err != nil {
				return fmt.Errorf("marshalling child params: %w", err)
			}
			planned[i] = domain.ChildPlanItem{Index: i, Params: params}
		}
		if err := e.appendNext(ctx, c.RunID, domain.ChildRunsPlanned{
			RunID: c.RunID, NodeID: c.NodeID, ExecID: c.ExecID, Items: planned,
		}); err != nil {
			return fmt.Errorf("recording spawn plan: %w", err)
		}
		plan = planned
	}

	return e.spawnNextBatch(ctx, def, definitionRef, nd, c.RunID, c.NodeID, c.ExecID, plan, dispatchCmds)
}

func findNode(def registry.Definition, nodeID string) (registry.NodeDef, bool) {
	for _, nd := range def.Nodes {
		if string(nd.ID) == nodeID {
			return nd, true
		}
	}
	return registry.NodeDef{}, false
}

// spawnPlan reads back ChildRunsPlanned for this exec, or nil if none has
// been recorded yet.
func (e *Engine) spawnPlan(ctx context.Context, runID, nodeID, execID string) ([]domain.ChildPlanItem, error) {
	envs, err := e.store.Read(ctx, runID)
	if err != nil {
		return nil, err
	}
	for _, env := range envs {
		if planned, ok := env.Event.(domain.ChildRunsPlanned); ok && planned.NodeID == nodeID && planned.ExecID == execID {
			return planned.Items, nil
		}
	}
	return nil, nil
}

// spawnedIndices reads back every ChildRunSpawned already recorded for
// this exec.
func (e *Engine) spawnedIndices(ctx context.Context, runID, nodeID, execID string) (map[int]string, error) {
	envs, err := e.store.Read(ctx, runID)
	if err != nil {
		return nil, err
	}
	out := map[int]string{}
	for _, env := range envs {
		if sp, ok := env.Event.(domain.ChildRunSpawned); ok && sp.NodeID == nodeID && sp.ExecID == execID {
			out[sp.Index] = sp.ChildRunID
		}
	}
	return out, nil
}

// spawnNextBatch spawns as many not-yet-spawned planned items as fit
// under strategy: bounded(N)'s live-child cap — "live" meaning spawned
// and not yet terminal. Called both at initial dispatch and again from
// handleChildRunFinished/reconcileSpawnJoin every time a live slot frees
// up, which is what makes bounded(N) a genuine progressive refill rather
// than a one-time cap on how many items forEach may produce.
// spawnNextBatch assumes its caller (dispatchSpawnChildrenLocked or
// reconcileSpawnJoinLocked) already holds spawnMu — it must never lock
// itself.
func (e *Engine) spawnNextBatch(ctx context.Context, def registry.Definition, definitionRef string, nd registry.NodeDef, runID, nodeID, execID string, plan []domain.ChildPlanItem, dispatchCmds bool) error {
	n, err := parseBoundedStrategy(nd.Spawn.Strategy)
	if err != nil {
		return err
	}
	spawned, err := e.spawnedIndices(ctx, runID, nodeID, execID)
	if err != nil {
		return err
	}

	live := 0
	for _, childRunID := range spawned {
		state, ok, err := e.store.GetRunState(ctx, childRunID)
		if err != nil {
			return err
		}
		if ok && !state.Status.Terminal() {
			live++
		}
	}

	childDefRef, err := resolveChildDefinitionPath(definitionRef, nd.Spawn.Workflow)
	if err != nil {
		return err
	}

	for _, item := range plan {
		if live >= n {
			break
		}
		if _, already := spawned[item.Index]; already {
			continue
		}
		triggerRef := formatSpawnTriggerRef(runID, nodeID, execID, item.Index)
		childRunID, err := e.spawner.SpawnChild(ctx, SpawnChildRequest{
			DefinitionRef: childDefRef,
			Params:        item.Params,
			TriggerRef:    triggerRef,
			ParentRunID:   runID,
		})
		if err != nil {
			return e.failSpawnNode(ctx, definitionRef, runID, nodeID, execID,
				fmt.Sprintf("spawning child %d: %v", item.Index, err), dispatchCmds)
		}
		if err := e.appendNext(ctx, runID, domain.ChildRunSpawned{
			RunID: runID, NodeID: nodeID, ExecID: execID, Index: item.Index, ChildRunID: childRunID,
		}); err != nil {
			return fmt.Errorf("recording child.run.spawned: %w", err)
		}
		live++
	}
	return nil
}

// resolveChildDefinitionPath resolves spawn.workflow (a bare name, e.g.
// "implement-task") to a real file: a sibling of the coordinator's own
// definition file, "<dir>/<workflow>.yaml" — the narrowest scheme that
// requires no named-workflow registry, which does not exist anywhere in
// this codebase yet (every other document addresses a definition by
// absolute file path). A real named-workflow registry is Future work.
func resolveChildDefinitionPath(parentDefinitionRef, workflow string) (string, error) {
	if workflow == "" {
		return "", fmt.Errorf("engine: spawn.workflow is empty")
	}
	dir := filepath.Dir(parentDefinitionRef)
	return filepath.Join(dir, workflow+".yaml"), nil
}

// resolveForEach resolves a "$.outputs.<nodeID>.<field>" reference against
// runID's own event log to the JSON array spawn.forEach names — the
// first real runtime consumer of the "$.outputs..." syntax
// checkInputRefs has only ever statically validated until now (L17). Only
// this one dotted-path shape is supported; anything else is a scope
// decision, not a silent partial JSONPath implementation.
func (e *Engine) resolveForEach(ctx context.Context, runID, path string) ([]json.RawMessage, error) {
	const prefix = "$.outputs."
	if !strings.HasPrefix(path, prefix) {
		return nil, fmt.Errorf("unsupported forEach reference %q (only $.outputs.<nodeID>.<field> is implemented)", path)
	}
	rest := strings.TrimPrefix(path, prefix)
	nodeID, fieldPath, _ := strings.Cut(rest, ".")
	if nodeID == "" {
		return nil, fmt.Errorf("forEach reference %q names no node", path)
	}

	output, err := e.lastNodeOutput(ctx, runID, nodeID)
	if err != nil {
		return nil, err
	}
	if output == nil {
		return nil, fmt.Errorf("node %q has not produced output yet", nodeID)
	}

	var decoded any = map[string]any(nil)
	if err := json.Unmarshal(output, &decoded); err != nil {
		return nil, fmt.Errorf("decoding %q's output: %w", nodeID, err)
	}
	if fieldPath != "" {
		for _, seg := range strings.Split(fieldPath, ".") {
			m, ok := decoded.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("forEach reference %q: %q is not an object", path, seg)
			}
			decoded, ok = m[seg]
			if !ok {
				return nil, fmt.Errorf("forEach reference %q: field %q not found", path, seg)
			}
		}
	}
	arr, ok := decoded.([]any)
	if !ok {
		return nil, fmt.Errorf("forEach reference %q does not resolve to an array", path)
	}
	items := make([]json.RawMessage, len(arr))
	for i, v := range arr {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("re-marshalling forEach item %d: %w", i, err)
		}
		items[i] = b
	}
	return items, nil
}

// lastNodeOutput scans runID's own event log for the most recent
// NodeOutputReceived or successfully-matched NodeWaitResolved recorded
// against nodeID and returns its inline Output. An OutputRef (L09's
// oversized-output blob reference) is a real gap this document leaves
// open — forEach over an oversized output fails loudly rather than
// silently resolving to nothing; see L17-child-runs.md's Future work.
func (e *Engine) lastNodeOutput(ctx context.Context, runID, nodeID string) (json.RawMessage, error) {
	envs, err := e.store.Read(ctx, runID)
	if err != nil {
		return nil, err
	}
	var out json.RawMessage
	for _, env := range envs {
		switch ev := env.Event.(type) {
		case domain.NodeOutputReceived:
			if ev.NodeID == nodeID && ev.SchemaValid {
				if ev.OutputRef != nil {
					return nil, fmt.Errorf("node %q's output is stored as an artifact reference, not inline — forEach over it is not yet supported", nodeID)
				}
				out = ev.Output
			}
		case domain.NodeWaitResolved:
			if ev.NodeID == nodeID && ev.Outcome == domain.WaitMatched && ev.SchemaValid {
				out = ev.Output
			}
		}
	}
	return out, nil
}

// spawnDepth resolves how many spawn levels deep runID already is, by
// reading its own TriggerReceived.TriggerRef (0 for a run not created by
// a spawn: node).
func (e *Engine) spawnDepth(ctx context.Context, runID string) (int, error) {
	ref, err := e.firstEventTriggerRef(ctx, runID)
	if err != nil {
		return 0, err
	}
	parentRunID, _, _, _, ok := parseSpawnTriggerRef(ref)
	if !ok {
		return 0, nil
	}
	parentDepth, err := e.spawnDepth(ctx, parentRunID)
	if err != nil {
		return 0, err
	}
	return parentDepth + 1, nil
}

func (e *Engine) firstEventTriggerRef(ctx context.Context, runID string) (string, error) {
	envs, err := e.store.Read(ctx, runID)
	if err != nil {
		return "", err
	}
	if len(envs) == 0 {
		return "", nil
	}
	trigger, ok := envs[0].Event.(domain.TriggerReceived)
	if !ok {
		return "", nil
	}
	return trigger.TriggerRef, nil
}

// handleChildRunFinished is shard.process's hook for L17: called every
// time ANY run reaches a terminal status, on the chance it is a spawned
// child. A non-spawn run's TriggerRef simply does not match the "spawn:"
// prefix and this returns immediately — cheap enough to call
// unconditionally rather than threading a "was this a child" flag through
// the shard.
func (e *Engine) handleChildRunFinished(ctx context.Context, childRunID string, _ domain.RunStatus) {
	ref, err := e.firstEventTriggerRef(ctx, childRunID)
	if err != nil {
		e.log.Error("spawn: reading child trigger ref failed", "runID", childRunID, "err", err)
		return
	}
	parentRunID, nodeID, execID, _, ok := parseSpawnTriggerRef(ref)
	if !ok {
		return
	}
	if err := e.reconcileSpawnJoin(ctx, parentRunID, nodeID, execID, false); err != nil {
		e.log.Error("spawn: reconciling join failed", "parentRunID", parentRunID, "nodeID", nodeID, "err", err)
	}
}

// reconcileSpawnJoin is the single place that recomputes a coordinator's
// join state from the log and acts on it — called live (from
// handleChildRunFinished, once per child completion, dispatchCmds=false:
// the shard already watching parentRunID's stream dispatches for itself)
// and at boot (Reconcile's catch-up pass, dispatchCmds=true: no shard
// exists yet, so any cmd a fold produces is lost unless this call
// dispatches it directly — resolveConversationWait/recoverLost's
// identical precedent). Always safe to call redundantly: every event it
// appends is derived from re-reading the current state of the log, not
// from in-memory bookkeeping that could be stale.
func (e *Engine) reconcileSpawnJoin(ctx context.Context, parentRunID, nodeID, execID string, dispatchCmds bool) error {
	e.spawnMu.Lock()
	defer e.spawnMu.Unlock()
	return e.reconcileSpawnJoinLocked(ctx, parentRunID, nodeID, execID, dispatchCmds)
}

// reconcileSpawnJoinLocked is reconcileSpawnJoin's body — the same
// spawnMu-must-already-be-held rule as dispatchSpawnChildrenLocked, and
// for the same reason: two children finishing near-simultaneously must
// not both decide, from a stale read, to resolve or refill the same
// join.
func (e *Engine) reconcileSpawnJoinLocked(ctx context.Context, parentRunID, nodeID, execID string, dispatchCmds bool) error {
	parentState, ok, err := e.store.GetRunState(ctx, parentRunID)
	if err != nil || !ok {
		return err
	}
	definitionRef, err := e.firstEventDefinitionRef(ctx, parentRunID)
	if err != nil {
		return err
	}
	def, err := e.loadDefinition(definitionRef)
	if err != nil {
		return err
	}
	nd, ok := findNode(def, nodeID)
	if !ok || nd.Spawn == nil || nd.Join == nil {
		return fmt.Errorf("engine: %s/%s is not a spawn/join node", parentRunID, nodeID)
	}

	plan, err := e.spawnPlan(ctx, parentRunID, nodeID, execID)
	if err != nil {
		return err
	}
	if plan == nil {
		// Nothing planned yet. Live, this is normal — CmdSpawnChildren's
		// own dispatch just hasn't reached the append yet, and it will.
		// At boot, it means the daemon crashed between the exec becoming
		// ExecWaiting and dispatchSpawnChildren ever running — re-enter
		// it directly, since nothing else will.
		if !dispatchCmds {
			return nil
		}
		return e.dispatchSpawnChildrenLocked(ctx, definitionRef, domain.CmdSpawnChildren{
			RunID: parentRunID, NodeID: nodeID, ExecID: execID,
		}, dispatchCmds)
	}
	spawned, err := e.spawnedIndices(ctx, parentRunID, nodeID, execID)
	if err != nil {
		return err
	}

	allPlannedSpawned := len(spawned) >= len(plan)
	terminalCount, failedCount := 0, 0
	for _, childRunID := range spawned {
		state, ok, err := e.store.GetRunState(ctx, childRunID)
		if err != nil {
			return err
		}
		if !ok || !state.Status.Terminal() {
			continue
		}
		terminalCount++
		if state.Status != domain.RunSucceeded {
			failedCount++
		}
	}

	if !allPlannedSpawned || terminalCount < len(spawned) {
		// The join is not done yet. If a failure has already appeared
		// and this coordinator degrades rather than fails, surface that
		// now rather than waiting for every straggler — 03-workflows.md:
		// "Degraded survives as a first-class state."
		if failedCount > 0 && nd.Join.OnChildFailure == "degrade" && parentState.Status == domain.RunRunning {
			if err := e.appendNext(ctx, parentRunID, domain.RunDegraded{
				RunID: parentRunID, Reason: fmt.Sprintf("%d of %d spawned children failed so far", failedCount, terminalCount),
			}); err != nil {
				return fmt.Errorf("recording run.degraded: %w", err)
			}
		}
		// Refill strategy: bounded(N) — a slot may have just freed up.
		return e.spawnNextBatch(ctx, def, definitionRef, nd, parentRunID, nodeID, execID, plan, dispatchCmds)
	}

	// Every planned child is spawned and terminal — the join itself is
	// done, one way or another.
	if failedCount == 0 {
		return e.resolveSpawnJoin(ctx, definitionRef, parentRunID, nodeID, execID, len(spawned), 0, dispatchCmds)
	}
	if nd.Join.OnChildFailure == "degrade" {
		if parentState.Status == domain.RunDegradedS {
			if err := e.appendNext(ctx, parentRunID, domain.RunDegradedResolved{RunID: parentRunID}); err != nil {
				return fmt.Errorf("recording run.degraded.resolved: %w", err)
			}
		}
		return e.resolveSpawnJoin(ctx, definitionRef, parentRunID, nodeID, execID, len(spawned), failedCount, dispatchCmds)
	}
	// onChildFailure: fail (the default) — the coordinator's own wait
	// resolves as a genuine failure, routed via the normal failure edge.
	return e.appendAndMaybeDispatch(ctx, definitionRef, parentRunID, domain.NodeWaitResolved{
		RunID: parentRunID, NodeID: nodeID, ExecID: execID, Outcome: domain.WaitFailed,
	}, dispatchCmds)
}

// failSpawnNode routes a spawn: node's dispatch-time failure (before any
// child even exists — no spawner configured, a bad forEach reference, a
// depth-limit breach, the spawn call itself erroring) through
// NodeWaitResolved{Outcome: WaitFailed}, the only legal ExecWaiting ->
// failure path (NodeExecutionFailed is illegal there — legalExecEvents
// only accepts it against ExecExecuting/ExecAdopted, since the exec was
// already moved to ExecWaiting by CmdEnterWait before CmdSpawnChildren
// ever dispatches). The specific reason is logged here rather than
// threaded through the event, matching the WaitTimedOut path's existing
// precedent — neither NodeWaitResolved nor handleFailureOutcome carries a
// free-form message today.
func (e *Engine) failSpawnNode(ctx context.Context, definitionRef, runID, nodeID, execID, reason string, dispatchCmds bool) error {
	e.log.Error("spawn: node failed", "runID", runID, "nodeID", nodeID, "execID", execID, "reason", reason)
	return e.appendAndMaybeDispatch(ctx, definitionRef, runID, domain.NodeWaitResolved{
		RunID: runID, NodeID: nodeID, ExecID: execID, Outcome: domain.WaitFailed,
	}, dispatchCmds)
}

// resolveSpawnJoin records the coordinator's own successful wait
// resolution — reached either with zero failures, or with some failures
// absorbed by onChildFailure: degrade.
func (e *Engine) resolveSpawnJoin(ctx context.Context, definitionRef, runID, nodeID, execID string, total, failed int, dispatchCmds bool) error {
	output, err := json.Marshal(map[string]any{"children": total, "failed": failed})
	if err != nil {
		return err
	}
	return e.appendAndMaybeDispatch(ctx, definitionRef, runID, domain.NodeWaitResolved{
		RunID: runID, NodeID: nodeID, ExecID: execID,
		Outcome: domain.WaitMatched, SchemaValid: true, Output: output,
	}, dispatchCmds)
}

// appendAndMaybeDispatch appends ev to runID's stream. When dispatchCmds
// is true (a Reconcile-time caller — no live shard will ever see this
// event), it also folds ev against the state captured immediately before
// the append and dispatches the resulting cmds itself, mirroring
// resolveConversationWait/recoverLost's identical precedent. When false
// (a live caller), the shard already watching runID's stream picks the
// event up via Subscribe and dispatches for itself — doing it here too
// would run it twice.
func (e *Engine) appendAndMaybeDispatch(ctx context.Context, definitionRef, runID string, ev domain.Event, dispatchCmds bool) error {
	var preState domain.RunState
	if dispatchCmds {
		state, ok, err := e.store.GetRunState(ctx, runID)
		if err != nil {
			return err
		}
		if ok {
			preState = state
		}
	}
	if err := e.appendNext(ctx, runID, ev); err != nil {
		return err
	}
	if !dispatchCmds {
		return nil
	}
	_, cmds, err := domain.Advance(preState, ev, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("folding %s: %w", ev.EventType(), err)
	}
	for _, cmd := range cmds {
		if err := e.dispatch(ctx, definitionRef, cmd); err != nil {
			return fmt.Errorf("dispatching cmd from %s: %w", ev.EventType(), err)
		}
	}
	return nil
}
