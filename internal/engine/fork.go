package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/events"
	"github.com/williamokano/kairos/internal/eventstore"
	"github.com/williamokano/kairos/internal/registry"
	"github.com/williamokano/kairos/internal/workspace"
)

// ErrWorkspaceDrift is 06-durability.md's refuse-by-default answer when
// Fork is asked for a sequence with no matching workspace.snapshot.taken
// event: "a fork whose filesystem silently came from a different moment
// gets read as a model difference when it is a state difference." Pass
// ForkRequest.AllowDrift to proceed anyway.
var ErrWorkspaceDrift = errors.New("engine: fork: no workspace snapshot exists at the requested sequence")

// ForkRequest is one kairos fork invocation.
type ForkRequest struct {
	FromRunID string
	// AtSequence is the reasoning cutoff — the new run's copied event
	// prefix is exactly events 1..AtSequence. Zero means "the latest
	// snapshot's sequence, if the source workflow uses a workspace,
	// else the source run's own latest event."
	AtSequence int
	// Overrides are applied to the copied TriggerReceived's Params —
	// 06-durability.md's `--set actor.implement=sonnet` example. Values
	// are strings, matching `kairos run --param k=v`'s existing
	// convention (internal/cli/run.go) — the same shape, not a richer
	// typed-override system.
	Overrides  map[string]string
	AllowDrift bool
}

// ForkResult is what Fork returns on success.
type ForkResult struct {
	NewRunID string
	// Drifted is true when AllowDrift let the fork proceed without an
	// exact snapshot at AtSequence — ActualSnapshotSeq names where the
	// live snapshot it took instead actually came from.
	Drifted           bool
	ActualSnapshotSeq int
}

// Fork implements 06-durability.md's "Fork and replay": copies
// FromRunID's event prefix 1..AtSequence into a brand-new run stream,
// restores the workspace (if any) from the nearest snapshot, applies
// Overrides to the copied trigger's params, and appends run.forked. The
// forked run's agent session is never restored (06-durability.md: "you
// cannot rewind a model conversation to turn 14 of 41") — the new run
// cold-starts whichever session machinery L08 already provides the next
// time an llm-kind node dispatches, seeded by nothing more than the
// copied event log itself (which already carries every node's recorded
// input/output — the "context digest" IS the folded RunState, not a
// separate artifact this method builds).
//
// The copied prefix is folded locally, once, to obtain the new run's
// final RunState and the Cmds its last event implies — those Cmds are
// dispatched exactly once, synchronously, here. The copied events are
// then appended to the store (which the live engine loop WILL observe on
// its ordinary subscribe path), primed beforehand via
// shard.primeForked so that second observation is a deliberate no-op —
// see shard.go's primedUpTo doc comment for why this is required at all:
// without it, the live loop would re-fold AND re-dispatch every
// already-completed node's CmdStartNode a second time.
func (e *Engine) Fork(ctx context.Context, req ForkRequest) (ForkResult, error) {
	fullEnvs, err := e.store.Read(ctx, req.FromRunID)
	if err != nil {
		return ForkResult{}, fmt.Errorf("reading source run %s: %w", req.FromRunID, err)
	}
	if len(fullEnvs) == 0 {
		return ForkResult{}, fmt.Errorf("engine: fork: run %s has no events", req.FromRunID)
	}
	trigger, ok := fullEnvs[0].Event.(domain.TriggerReceived)
	if !ok {
		return ForkResult{}, fmt.Errorf("engine: fork: run %s's first event is not TriggerReceived", req.FromRunID)
	}

	def, err := e.loadDefinition(trigger.DefinitionRef)
	if err != nil {
		return ForkResult{}, fmt.Errorf("loading source definition: %w", err)
	}
	needsWorkspace := false
	for _, nd := range def.Nodes {
		if nd.Workspace == registry.WorkspaceWrite {
			needsWorkspace = true
			break
		}
	}

	latestSeq := fullEnvs[len(fullEnvs)-1].Sequence
	var snapshotSeqs []int
	snapshotsBySeq := map[int]domain.WorkspaceSnapshotTaken{}
	for _, env := range fullEnvs {
		if s, ok := env.Event.(domain.WorkspaceSnapshotTaken); ok {
			snapshotSeqs = append(snapshotSeqs, env.Sequence)
			snapshotsBySeq[env.Sequence] = s
		}
	}

	atSeq := req.AtSequence
	if atSeq == 0 {
		if needsWorkspace && len(snapshotSeqs) > 0 {
			atSeq = snapshotSeqs[len(snapshotSeqs)-1]
		} else {
			atSeq = latestSeq
		}
	}
	if atSeq > latestSeq {
		return ForkResult{}, fmt.Errorf("engine: fork: sequence %d exceeds run %s's length %d", atSeq, req.FromRunID, latestSeq)
	}

	result := ForkResult{ActualSnapshotSeq: atSeq}
	snap, exact := snapshotsBySeq[atSeq]
	var lineageSnap *domain.WorkspaceSnapshotTaken
	if exact {
		lineageSnap = &snap
	} else if needsWorkspace {
		if !req.AllowDrift {
			return ForkResult{}, ErrWorkspaceDrift
		}
		result.Drifted = true
		origWS := e.workspaces.Existing(req.FromRunID)
		live, err := e.workspaces.SnapshotGitRef(ctx, origWS, latestSeq)
		if err != nil {
			return ForkResult{}, fmt.Errorf("taking drift snapshot: %w", err)
		}
		result.ActualSnapshotSeq = latestSeq
		lineageSnap = &domain.WorkspaceSnapshotTaken{
			RunID: req.FromRunID, AtSequence: latestSeq, Kind: "git", Ref: live.Ref, SHA: live.SHA,
		}
	}

	newRunID := "run_" + ulid.Make().String()
	lineageRoot := lineageRootOf(fullEnvs, req.FromRunID)

	copied := make([]domain.Event, 0, atSeq)
	occurredAt := make([]time.Time, 0, atSeq)
	for _, env := range fullEnvs {
		if env.Sequence > atSeq {
			break
		}
		ev := rekeyRunID(env.Event, req.FromRunID, newRunID)
		copied = append(copied, ev)
		occurredAt = append(occurredAt, env.OccurredAt)
	}
	if len(req.Overrides) > 0 {
		if tr, ok := copied[0].(domain.TriggerReceived); ok {
			merged, err := mergeParams(tr.Params, req.Overrides)
			if err != nil {
				return ForkResult{}, fmt.Errorf("applying overrides: %w", err)
			}
			tr.Params = merged
			copied[0] = tr
		}
	}

	// Replay the copied prefix locally, once, purely — this both
	// establishes the new run's final RunState (for priming) and yields
	// the continuation Cmds (to dispatch, once, below). lastCmds tracks
	// the most recent NON-EMPTY cmds, not simply the very last event's:
	// a copy boundary chosen at a node-completion snapshot (this
	// document's own maybeSnapshotWorkspace hook appends
	// WorkspaceSnapshotTaken right after NodeOutputReceived) would
	// otherwise lose NodeOutputReceived's real CmdStartNode/routing
	// behind a trailing no-op bookkeeping fold.
	state := domain.RunState{}
	var lastCmds []domain.Cmd
	for i, ev := range copied {
		next, cmds, err := domain.Advance(state, ev, occurredAt[i])
		if err != nil {
			return ForkResult{}, fmt.Errorf("replaying copied event %d (%s): %w", i+1, ev.EventType(), err)
		}
		state = next
		if len(cmds) > 0 {
			lastCmds = cmds
		}
	}
	// A run forked at (or past) its own completion has nothing left to
	// continue — lastCmds's "most recent non-empty cmds" heuristic exists
	// to see past a trailing no-op bookkeeping event (see the doc comment
	// above), but it cannot distinguish that case from "the run's real
	// work finished and its terminal transition simply produced zero
	// cmds": both leave lastCmds holding a real, but now STALE, cmd from
	// earlier in the sequence (found via TestIntegration_forkAndCompareCLI:
	// forking a fully succeeded run re-dispatched an already-folded
	// CmdEvaluateGates against the new run's already-terminal exec,
	// producing a real domain.ErrIllegalTransition — logged and dropped,
	// not fatal, but never should have been attempted).
	if state.Status.Terminal() {
		lastCmds = nil
	}

	e.shardFor(newRunID).primeForked(ctx, newRunID, state, trigger.DefinitionRef, atSeq)

	if needsWorkspace && lineageSnap != nil {
		newWS, err := e.workspaces.Provision(ctx, newRunID, e.workspaceRepo)
		if err != nil {
			return ForkResult{}, fmt.Errorf("provisioning forked workspace: %w", err)
		}
		origWS := e.workspaces.Existing(req.FromRunID)
		if err := e.workspaces.RestoreGitRef(ctx, newWS, origWS.Dir, workspace.Snapshot{Ref: lineageSnap.Ref, SHA: lineageSnap.SHA}); err != nil {
			return ForkResult{}, fmt.Errorf("restoring forked workspace: %w", err)
		}
	}

	// Append the copied prefix one event at a time so each retains its
	// ORIGINAL OccurredAt (AppendMeta covers a whole batch with one
	// timestamp, and 06-durability.md's "restorable exactly" promise
	// includes the historical record, not "now" for every copied row).
	for i, ev := range copied {
		if _, err := e.store.AppendIf(ctx, newRunID, i, []domain.Event{ev}, eventstore.AppendMeta{
			Actor: "fork", CorrelationID: newRunID, OccurredAt: occurredAt[i],
		}); err != nil {
			return ForkResult{}, fmt.Errorf("appending copied event %d: %w", i+1, err)
		}
	}

	forkedEv := domain.RunForked{
		RunID: newRunID, FromRunID: req.FromRunID, LineageRoot: lineageRoot,
		AtSequence: atSeq, Overrides: req.Overrides,
	}
	if _, err := e.store.AppendIf(ctx, newRunID, atSeq, []domain.Event{forkedEv}, eventstore.AppendMeta{
		Actor: "fork", CorrelationID: newRunID, OccurredAt: time.Now().UTC(),
	}); err != nil {
		return ForkResult{}, fmt.Errorf("appending run.forked: %w", err)
	}
	nextSeq := atSeq + 1

	if result.Drifted {
		if _, err := e.store.AppendIf(ctx, newRunID, nextSeq, []domain.Event{domain.ForkWorkspaceDrifted{
			RunID: newRunID, RequestedSeq: atSeq, ActualSeq: result.ActualSnapshotSeq,
		}}, eventstore.AppendMeta{Actor: "fork", CorrelationID: newRunID, OccurredAt: time.Now().UTC()}); err != nil {
			return ForkResult{}, fmt.Errorf("appending fork.workspace.drifted: %w", err)
		}
	}

	for _, cmd := range lastCmds {
		if err := e.dispatch(ctx, trigger.DefinitionRef, cmd); err != nil {
			e.log.Error("fork: dispatching continuation cmd failed", "newRunID", newRunID, "err", err)
		}
	}

	result.NewRunID = newRunID
	return result, nil
}

// maybeSnapshotWorkspace takes a git-ref snapshot (ADR 0006 layer 1) at a
// node-completion boundary, for a workspace: write node whose output was
// just recorded — the "node boundaries where the workspace is writable"
// ADR 0006 names as the snapshot cadence. Best-effort: a snapshot
// failure is logged, never fails the node it rides along with (a missing
// snapshot only costs a future Fork its exact-match fast path — Fork
// still works via --allow-drift).
func (e *Engine) maybeSnapshotWorkspace(ctx context.Context, isWorkspaceWrite bool, runID, nodeID, execID string) {
	if !isWorkspaceWrite {
		return
	}
	seq, err := e.currentSeq(ctx, runID)
	if err != nil {
		e.log.Error("snapshot: reading current sequence failed", "runID", runID, "err", err)
		return
	}
	ws := e.workspaces.Existing(runID)
	snap, err := e.workspaces.SnapshotGitRef(ctx, ws, seq)
	if err != nil {
		e.log.Error("snapshot: SnapshotGitRef failed", "runID", runID, "nodeID", nodeID, "err", err)
		return
	}
	if err := e.appendNext(ctx, runID, domain.WorkspaceSnapshotTaken{
		RunID: runID, NodeID: nodeID, ExecID: execID, AtSequence: seq,
		Label: fmt.Sprintf("@%s-%s", nodeID, execID), Kind: "git", Ref: snap.Ref, SHA: snap.SHA,
	}); err != nil {
		e.log.Error("snapshot: recording workspace.snapshot.taken failed", "runID", runID, "nodeID", nodeID, "err", err)
	}
}

// lineageRootFor resolves runID's lineage root for effect idempotency
// (internal/effect.IdempotencyKey, L12) — a forked run's own lineage
// root, not its own runID, so an effect action a fork re-attempts
// updates the same external state the original run's attempt did,
// instead of duplicating it (06-durability.md: "gh.pr.create on a fork
// updates the original PR rather than opening a second"). A read error
// degrades to runID itself — the same fail-open posture
// mustDefinitionRef already uses for a comparable read, since refusing
// to dispatch an effect over a lineage-lookup hiccup would be a worse
// failure mode than a very occasionally-duplicated non-forked effect.
func (e *Engine) lineageRootFor(ctx context.Context, runID string) string {
	envs, err := e.store.Read(ctx, runID)
	if err != nil {
		return runID
	}
	return lineageRootOf(envs, runID)
}

// lineageRootOf walks runID's own event prefix looking for a RunForked
// event recording where IT was forked from, recursively resolving to the
// original, never-forked ancestor. internal/effect's IdempotencyKey uses
// this so a fork's effect actions update the lineage's external state
// rather than duplicating it (L12's "lineage" placeholder, made real).
func lineageRootOf(envs []events.Envelope, runID string) string {
	for _, env := range envs {
		if rf, ok := env.Event.(domain.RunForked); ok {
			return rf.LineageRoot
		}
	}
	return runID
}

// rekeyRunID returns a copy of ev with every exported "RunID" string
// field equal to oldRunID rewritten to newRunID — every event type this
// document's fork touches, uniformly, without a per-type switch (30+
// event types carry a RunID field; a hand-written case for each would be
// the exact kind of parallel machinery AGENTS §7 warns against).
func rekeyRunID(ev domain.Event, oldRunID, newRunID string) domain.Event {
	v := reflect.ValueOf(ev)
	if v.Kind() != reflect.Struct {
		return ev
	}
	nv := reflect.New(v.Type()).Elem()
	nv.Set(v)
	if f := nv.FieldByName("RunID"); f.IsValid() && f.CanSet() && f.Kind() == reflect.String && f.String() == oldRunID {
		f.SetString(newRunID)
	}
	return nv.Interface().(domain.Event)
}

// mergeParams decodes base (a TriggerReceived's Params, possibly empty),
// applies overrides on top (string values, matching `kairos run --param
// k=v`'s existing shape), and re-encodes.
func mergeParams(base json.RawMessage, overrides map[string]string) (json.RawMessage, error) {
	m := map[string]any{}
	if len(base) > 0 {
		if err := json.Unmarshal(base, &m); err != nil {
			return nil, fmt.Errorf("decoding base params: %w", err)
		}
	}
	for k, v := range overrides {
		m[k] = v
	}
	return json.Marshal(m)
}
