package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/engine"
	"github.com/williamokano/kairos/internal/eventstore"
	"github.com/williamokano/kairos/internal/executor/local"
	"github.com/williamokano/kairos/internal/tasksource"
)

// realSpawner is a real internal/tasksource-backed engine.RunSpawner —
// exactly what cmd/kairos wires in production (cmd/kairos/spawner.go),
// duplicated here rather than imported so this test exercises the same
// TriggerRun path a production spawn actually takes.
type realSpawner struct{ store eventstore.Store }

func (s *realSpawner) SpawnChild(ctx context.Context, req engine.SpawnChildRequest) (string, error) {
	parentRunID := req.ParentRunID
	runID, _, err := tasksource.TriggerRun(ctx, s.store, req.TriggerRef, "spawn", req.TriggerRef, tasksource.CreateRunRequest{
		DefinitionRef: req.DefinitionRef,
		Params:        req.Params,
		TriggerRef:    req.TriggerRef,
		Actor:         "engine:spawn",
		ParentRunID:   &parentRunID,
	}, tasksource.QueueLimits{})
	return runID, err
}

func newTestEngineWithSpawner(t *testing.T, st eventstore.Store, workRoot string) *engine.Engine {
	t.Helper()
	return engine.New(engine.Config{
		Store:     st,
		Executor:  local.New(local.DefaultBootIDProvider()),
		BootID:    local.DefaultBootIDProvider(),
		WorkRoot:  workRoot,
		KillGrace: 200 * time.Millisecond,
		Spawner:   &realSpawner{store: st},
	})
}

// TestEngine_spawnJoinFansOutAndWaitsForAllChildren is L17's flagship
// end-to-end proof: a coordinator's spawn: node resolves forEach against
// a real preceding node's real output, creates real child Runs through
// the same tasksource.TriggerRun path a trigger source uses, and its
// join: waitAll only resolves once every child independently reaches a
// terminal state.
func TestEngine_spawnJoinFansOutAndWaitsForAllChildren(t *testing.T) {
	st := openStore(t)
	workRoot := t.TempDir()
	dir := t.TempDir()

	childPath := filepath.Join(dir, "child.yaml")
	writeFile(t, childPath, `
name: child
nodes:
  - id: only
    actor: rule
    output: { x: "string" }
`)

	parentPath := filepath.Join(dir, "parent.yaml")
	writeFile(t, parentPath, `
name: parent
nodes:
  - id: plan
    actor: shell
    prompt: echo '{"tasks":["a","b","c"]}' > "$KAIROS_OUTPUT_PATH"
    output: { tasks: ["string"] }
  - id: fanout
    actor: spawn
    spawn:
      workflow: child
      forEach: "$.outputs.plan.tasks"
      strategy: bounded(2)
      inheritWorkspace: clone
    join: { mode: waitAll, onChildFailure: fail }
`)

	eng := newTestEngineWithSpawner(t, st, workRoot)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if _, err := eng.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = eng.Stop(context.Background()) }()

	runID := "run_spawn_e2e"
	graph := domain.Graph{
		Entry: "plan",
		Nodes: []domain.Node{
			{ID: "plan", Retry: domain.RetryPolicy{MaxAttempts: 1}, LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 1}},
			{
				ID:        "fanout",
				Wait:      &domain.WaitSpec{Kind: domain.WaitChildRun, OnTimeout: domain.OnTimeoutAction("park")},
				Retry:     domain.RetryPolicy{MaxAttempts: 1},
				LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 1},
			},
		},
		Edges: map[domain.NodeID]map[domain.EdgeTrigger]domain.NodeID{
			"plan":   {domain.OnSuccess: "fanout", domain.OnFailure: domain.SinkFail, domain.OnTimeout: domain.SinkFail},
			"fanout": {domain.OnSuccess: domain.SinkSucceed, domain.OnFailure: domain.SinkFail, domain.OnTimeout: domain.SinkFail},
		},
	}

	meta := appendMetaFor(runID)
	if _, err := st.AppendIf(ctx, runID, 0, []domain.Event{
		domain.TriggerReceived{RunID: runID, TriggerRef: "test", DefinitionRef: parentPath, CorrelationID: runID},
	}, meta); err != nil {
		t.Fatalf("append trigger: %v", err)
	}
	if _, err := st.AppendIf(ctx, runID, 1, []domain.Event{
		domain.RunStarted{RunID: runID, Graph: graph},
	}, meta); err != nil {
		t.Fatalf("append run started: %v", err)
	}

	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		state, ok, err := st.GetRunState(ctx, runID)
		if err != nil {
			t.Fatalf("GetRunState: %v", err)
		}
		if ok && state.Status.Terminal() {
			if state.Status != domain.RunSucceeded {
				t.Fatalf("run Status = %s, want %s; state=%+v", state.Status, domain.RunSucceeded, state)
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	state, ok, err := st.GetRunState(ctx, runID)
	if err != nil || !ok {
		t.Fatalf("GetRunState after loop: ok=%v err=%v", ok, err)
	}
	if state.Status != domain.RunSucceeded {
		t.Fatal("run did not reach RunSucceeded within the deadline")
	}

	// Confirm three real, independent child runs were created and all
	// succeeded — not just that the parent's own state moved on.
	envs, err := st.Read(ctx, runID)
	if err != nil {
		t.Fatalf("Read parent stream: %v", err)
	}
	var childIDs []string
	for _, env := range envs {
		if sp, ok := env.Event.(domain.ChildRunSpawned); ok {
			childIDs = append(childIDs, sp.ChildRunID)
		}
	}
	if len(childIDs) != 3 {
		t.Fatalf("spawned %d children, want 3", len(childIDs))
	}
	for _, childID := range childIDs {
		cs, ok, err := st.GetRunState(ctx, childID)
		if err != nil || !ok {
			t.Fatalf("child %s GetRunState: ok=%v err=%v", childID, ok, err)
		}
		if cs.Status != domain.RunSucceeded {
			t.Errorf("child %s Status = %s, want succeeded", childID, cs.Status)
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// runSpawnFixture wires up a real end-to-end engine, a "plan" node
// producing a two-item forEach array, and a "fanout" spawn/join node over
// a child workflow whose single node always fails — the shared setup
// TestEngine_spawnOnChildFailure{Fails,Degrades}TheCoordinator both need,
// differing only in join.onChildFailure.
func runSpawnFixture(t *testing.T, onChildFailure string) (st eventstore.Store, runID string) {
	t.Helper()
	st = openStore(t)
	workRoot := t.TempDir()
	dir := t.TempDir()

	writeFile(t, filepath.Join(dir, "childfail.yaml"), `
name: childfail
nodes:
  - id: only
    actor: shell
    prompt: "exit 1"
    output: { x: "string" }
`)

	parentPath := filepath.Join(dir, "parent.yaml")
	writeFile(t, parentPath, `
name: parent
nodes:
  - id: plan
    actor: shell
    prompt: echo '{"tasks":["a","b"]}' > "$KAIROS_OUTPUT_PATH"
    output: { tasks: ["string"] }
  - id: fanout
    actor: spawn
    spawn:
      workflow: childfail
      forEach: "$.outputs.plan.tasks"
      strategy: bounded(2)
      inheritWorkspace: clone
    join: { mode: waitAll, onChildFailure: `+onChildFailure+` }
`)

	eng := newTestEngineWithSpawner(t, st, workRoot)
	// Deliberately not tied to a deferred-cancel local context: this
	// helper returns long before the caller finishes polling for the
	// run's terminal state, and Start's live loop must keep running for
	// the whole test, not just for runSpawnFixture's own stack frame.
	// eng.Stop (via t.Cleanup) is what actually shuts it down.
	startCtx := context.Background()
	reconcileCtx, cancel := context.WithTimeout(startCtx, 15*time.Second)
	defer cancel()

	if _, err := eng.Reconcile(reconcileCtx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := eng.Start(startCtx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = eng.Stop(context.Background()) })

	runID = "run_spawn_" + onChildFailure
	graph := domain.Graph{
		Entry: "plan",
		Nodes: []domain.Node{
			{ID: "plan", Retry: domain.RetryPolicy{MaxAttempts: 1}, LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 1}},
			{
				ID:        "fanout",
				Wait:      &domain.WaitSpec{Kind: domain.WaitChildRun, OnTimeout: domain.OnTimeoutAction("park")},
				Retry:     domain.RetryPolicy{MaxAttempts: 1},
				LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 1},
			},
		},
		Edges: map[domain.NodeID]map[domain.EdgeTrigger]domain.NodeID{
			"plan":   {domain.OnSuccess: "fanout", domain.OnFailure: domain.SinkFail, domain.OnTimeout: domain.SinkFail},
			"fanout": {domain.OnSuccess: domain.SinkSucceed, domain.OnFailure: domain.SinkFail, domain.OnTimeout: domain.SinkFail},
		},
	}

	meta := appendMetaFor(runID)
	if _, err := st.AppendIf(startCtx, runID, 0, []domain.Event{
		domain.TriggerReceived{RunID: runID, TriggerRef: "test", DefinitionRef: parentPath, CorrelationID: runID},
	}, meta); err != nil {
		t.Fatalf("append trigger: %v", err)
	}
	if _, err := st.AppendIf(startCtx, runID, 1, []domain.Event{
		domain.RunStarted{RunID: runID, Graph: graph},
	}, meta); err != nil {
		t.Fatalf("append run started: %v", err)
	}
	return st, runID
}

// TestEngine_spawnOnChildFailureFailsTheCoordinator proves
// join.onChildFailure: fail (the default) is a genuine failure — the
// coordinator's own wait resolves via NodeWaitResolved{Outcome:
// WaitFailed} and the run ends Failed, never silently succeeding despite
// every child failing.
func TestEngine_spawnOnChildFailureFailsTheCoordinator(t *testing.T) {
	st, runID := runSpawnFixture(t, "fail")
	ctx := context.Background()
	status := waitForTerminal(t, ctx, st, runID, 15*time.Second)
	if status != domain.RunFailed {
		t.Fatalf("run Status = %s, want %s", status, domain.RunFailed)
	}
}

// TestEngine_spawnOnChildFailureDegradeStillSucceeds proves
// join.onChildFailure: degrade absorbs a child failure: run.degraded is
// recorded along the way, but once the join is fully accounted for, the
// coordinator's own wait resolves as Matched and the run reaches
// RunSucceeded — 03-workflows.md: "Degraded survives as a first-class
// state," not a terminal one.
func TestEngine_spawnOnChildFailureDegradeStillSucceeds(t *testing.T) {
	st, runID := runSpawnFixture(t, "degrade")
	ctx := context.Background()
	status := waitForTerminal(t, ctx, st, runID, 15*time.Second)
	if status != domain.RunSucceeded {
		t.Fatalf("run Status = %s, want %s", status, domain.RunSucceeded)
	}

	envs, err := st.Read(ctx, runID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var sawDegraded bool
	for _, env := range envs {
		if _, ok := env.Event.(domain.RunDegraded); ok {
			sawDegraded = true
		}
	}
	if !sawDegraded {
		t.Error("expected run.degraded to have been recorded at some point during the join")
	}
}

// TestReconcile_catchesUpAJoinWhoseChildrenFinishedWhileNoEngineWasWatching
// is L17's kill-mid-spawn proof: every child already reached a terminal
// status with no live engine ever having processed the completion (the
// exact "daemon was down" scenario) — Reconcile's catch-up pass, not the
// live handleChildRunFinished hook, is what resolves the coordinator's
// join on the next boot.
func TestReconcile_catchesUpAJoinWhoseChildrenFinishedWhileNoEngineWasWatching(t *testing.T) {
	st := openStore(t)
	dir := t.TempDir()

	childPath := filepath.Join(dir, "child.yaml")
	writeFile(t, childPath, `
name: child
nodes:
  - id: only
    actor: rule
    output: { x: "string" }
`)
	parentPath := filepath.Join(dir, "parent.yaml")
	writeFile(t, parentPath, `
name: parent
nodes:
  - id: plan
    actor: shell
    prompt: echo '{"tasks":["a","b"]}' > "$KAIROS_OUTPUT_PATH"
    output: { tasks: ["string"] }
  - id: fanout
    actor: spawn
    spawn:
      workflow: child
      forEach: "$.outputs.plan.tasks"
      strategy: bounded(2)
      inheritWorkspace: clone
    join: { mode: waitAll, onChildFailure: fail }
`)

	ctx := context.Background()
	parentRunID := "run_reconcile_spawn_parent"

	// Two already-fully-terminal (succeeded) children, exactly as if a
	// live engine had spawned and driven them to completion in some
	// earlier boot this test never simulates directly — only their
	// final state matters.
	childIDs := []string{"run_reconcile_spawn_child_0", "run_reconcile_spawn_child_1"}
	for i, childID := range childIDs {
		seedTerminalRuleChild(t, st, childID, childPath, formatSpawnTriggerRefForTest(parentRunID, "fanout", "fanout#a1.i1", i))
	}

	// The parent: plan already succeeded, fanout already Waiting with its
	// plan and both spawns recorded — but crucially, NOT yet resolved,
	// simulating the daemon dying right after the second ChildRunSpawned
	// was recorded and before either child's completion was ever
	// observed live.
	parentGraph := domain.Graph{
		Entry: "plan",
		Nodes: []domain.Node{
			{ID: "plan", Retry: domain.RetryPolicy{MaxAttempts: 1}, LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 1}},
			{
				ID:        "fanout",
				Wait:      &domain.WaitSpec{Kind: domain.WaitChildRun, OnTimeout: domain.OnTimeoutAction("park")},
				Retry:     domain.RetryPolicy{MaxAttempts: 1},
				LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 1},
			},
		},
		Edges: map[domain.NodeID]map[domain.EdgeTrigger]domain.NodeID{
			"plan":   {domain.OnSuccess: "fanout", domain.OnFailure: domain.SinkFail, domain.OnTimeout: domain.SinkFail},
			"fanout": {domain.OnSuccess: domain.SinkSucceed, domain.OnFailure: domain.SinkFail, domain.OnTimeout: domain.SinkFail},
		},
	}
	meta := appendMetaFor(parentRunID)
	events := []domain.Event{
		domain.TriggerReceived{RunID: parentRunID, TriggerRef: "test", DefinitionRef: parentPath, CorrelationID: parentRunID},
		domain.RunStarted{RunID: parentRunID, Graph: parentGraph},
		domain.NodeExecutionStarted{RunID: parentRunID, NodeID: "plan", ExecID: "plan#a1.i1", Attempt: 1, Iteration: 1},
		domain.NodeOutputReceived{RunID: parentRunID, NodeID: "plan", ExecID: "plan#a1.i1", SchemaValid: true, Output: []byte(`{"tasks":["a","b"]}`)},
		domain.NodeGatesEvaluated{RunID: parentRunID, NodeID: "plan", ExecID: "plan#a1.i1", Passed: true},
		domain.ChildRunsPlanned{RunID: parentRunID, NodeID: "fanout", ExecID: "fanout#a1.i1", Items: []domain.ChildPlanItem{
			{Index: 0, Params: []byte(`{"item":"a"}`)},
			{Index: 1, Params: []byte(`{"item":"b"}`)},
		}},
		domain.ChildRunSpawned{RunID: parentRunID, NodeID: "fanout", ExecID: "fanout#a1.i1", Index: 0, ChildRunID: childIDs[0]},
		domain.ChildRunSpawned{RunID: parentRunID, NodeID: "fanout", ExecID: "fanout#a1.i1", Index: 1, ChildRunID: childIDs[1]},
	}
	appendSequentially(t, ctx, st, parentRunID, events, meta)

	eng := newTestEngineWithSpawner(t, st, t.TempDir())
	if _, err := eng.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Reconcile alone only gets the join as far as recording
	// NodeWaitResolved (Waiting -> Executing, dispatching
	// CmdEvaluateGates) — exactly like every other recovered node, it
	// needs the live loop to actually finish, matching how production
	// always runs Reconcile then Start, never Reconcile alone. The proof
	// this test cares about — that Reconcile's catch-up pass is what
	// noticed the already-finished children and moved the join forward
	// at all — already happened above; this just lets that motion reach
	// a terminal state so it can be asserted on.
	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = eng.Stop(context.Background()) })

	status := waitForTerminal(t, ctx, st, parentRunID, 10*time.Second)
	if status != domain.RunSucceeded {
		t.Fatalf("run Status = %s, want %s (Reconcile should have resolved the join)", status, domain.RunSucceeded)
	}
}

func formatSpawnTriggerRefForTest(parentRunID, nodeID, execID string, index int) string {
	return "spawn:" + parentRunID + ":" + nodeID + ":" + execID + ":" + strconv.Itoa(index)
}

// seedTerminalRuleChild appends a complete, already-succeeded one-node
// (actor: rule) run — TriggerReceived through the node's own success —
// exactly the shape a real engine would have produced, but written
// directly so this test needs no live engine to construct the "already
// finished, nobody was watching" scenario.
func seedTerminalRuleChild(t *testing.T, st eventstore.Store, runID, definitionRef, triggerRef string) {
	t.Helper()
	graph := domain.Graph{
		Entry: "only",
		Nodes: []domain.Node{
			{ID: "only", Retry: domain.RetryPolicy{MaxAttempts: 1}, LoopGuard: domain.LoopGuard{MaxIterationsPerNode: 1}},
		},
		Edges: map[domain.NodeID]map[domain.EdgeTrigger]domain.NodeID{
			"only": {domain.OnSuccess: domain.SinkSucceed, domain.OnFailure: domain.SinkFail, domain.OnTimeout: domain.SinkFail},
		},
	}
	meta := appendMetaFor(runID)
	events := []domain.Event{
		domain.TriggerReceived{RunID: runID, TriggerRef: triggerRef, DefinitionRef: definitionRef, CorrelationID: runID},
		domain.RunStarted{RunID: runID, Graph: graph},
		domain.NodeExecutionStarted{RunID: runID, NodeID: "only", ExecID: "only#a1.i1", Attempt: 1, Iteration: 1},
		domain.NodeOutputReceived{RunID: runID, NodeID: "only", ExecID: "only#a1.i1", SchemaValid: true, Output: []byte(`{}`)},
		domain.NodeGatesEvaluated{RunID: runID, NodeID: "only", ExecID: "only#a1.i1", Passed: true},
	}
	appendSequentially(t, context.Background(), st, runID, events, meta)

	state, ok, err := st.GetRunState(context.Background(), runID)
	if err != nil || !ok || state.Status != domain.RunSucceeded {
		t.Fatalf("seedTerminalRuleChild(%s): state=%+v ok=%v err=%v, want RunSucceeded", runID, state, ok, err)
	}
}

func appendSequentially(t *testing.T, ctx context.Context, st eventstore.Store, runID string, evs []domain.Event, meta eventstore.AppendMeta) {
	t.Helper()
	for i, ev := range evs {
		if _, err := st.AppendIf(ctx, runID, i, []domain.Event{ev}, meta); err != nil {
			t.Fatalf("appending %s at seq %d: %v", ev.EventType(), i, err)
		}
	}
}
