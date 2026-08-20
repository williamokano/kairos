package tasksource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/eventstore"
	"github.com/williamokano/kairos/internal/registry"
)

// CreateRunRequest is every trigger source's input to the one code path
// that turns a decision to run something into a Run. TriggerRef is
// 08-triggers.md's promise made concrete: `event.go`'s TriggerReceived
// doc comment says "L16 gives this structure" — the convention this
// package establishes is `"<kind>:<detail>"`, e.g. "cli:kairos-run",
// "inbox:<dedupeKey>", "poll:<sourceID>:<itemID>", "cron:<sourceID>",
// "webhook:<sourceID>:<dedupeKey>".
type CreateRunRequest struct {
	DefinitionRef string
	Params        json.RawMessage
	TriggerRef    string
	Actor         string // AppendMeta.Actor — "cli", "trigger:inbox", "trigger:poll", etc.
	ParentRunID   *string
}

// QueueLimits are 08-triggers.md's "rejecting rather than dropping"
// thresholds. Zero disables a check (used by internal/api's direct
// `kairos run`, which 08-triggers.md exempts: "queued >= maxQueued" is
// about trigger-created backlog, not a user typing a command right now —
// see L16-triggers.md's Documented decisions for why CLI-created runs
// pass QueueLimits{} unchecked while every trigger source does not).
type QueueLimits struct {
	MaxQueued        int
	MaxOpenDecisions int
}

var (
	// ErrQueueFull is returned when MaxQueued non-terminal runs already
	// exist — 08-triggers.md: "queued >= maxQueued (40) → REJECT, and
	// report the rejection upstream via ack."
	ErrQueueFull = errors.New("tasksource: run queue is full (maxQueued exceeded)")
	// ErrTooManyOpenDecisions is returned when MaxOpenDecisions
	// wait:human node executions already exist — "backpressure on the
	// scarcest resource in the system."
	ErrTooManyOpenDecisions = errors.New("tasksource: too many open human decisions (maxOpenDecisions exceeded)")
)

// ValidationError wraps a CreateRun failure caused by the definition
// itself (missing file, parse/publish-validation failure, unresolvable
// graph) — never by the store. Callers (internal/api) use errors.As to
// tell "the caller's input was bad" (422) apart from "the store/domain
// layer failed" (500, the crash-gap case L04's own regression test
// checks for).
type ValidationError struct{ err error }

func (e *ValidationError) Error() string { return e.err.Error() }
func (e *ValidationError) Unwrap() error { return e.err }

// CreateRun is 08-triggers.md's "one code path out." internal/api's POST
// /runs handler and every trigger source in this package call this and
// nothing else to start a run — internal/engine's actor dispatch code
// never imports this package (TestArchitecture_runCreationNotReachableFromActors
// in internal/archtest enforces that as a real import-graph fact, not a
// convention).
//
// It performs the same two-AppendIf sequence L04's handleCreateRun
// established (TriggerReceived, then a synchronous fold-only Advance to
// resolve DefinitionRef into a Graph, then RunStarted) — decision #1 in
// L04-daemon-api-cli.md, unchanged here.
func CreateRun(ctx context.Context, store eventstore.Store, req CreateRunRequest, limits QueueLimits) (runID string, status domain.RunStatus, err error) {
	if err := checkQueueLimits(ctx, store, limits); err != nil {
		return "", "", err
	}

	absPath, err := filepath.Abs(req.DefinitionRef)
	if err != nil {
		return "", "", &ValidationError{fmt.Errorf("resolving definitionRef: %w", err)}
	}
	def, err := registry.Load(absPath)
	if err != nil {
		return "", "", &ValidationError{fmt.Errorf("loading definition: %w", err)}
	}
	graph, err := registry.ProjectGraph(def)
	if err != nil {
		return "", "", &ValidationError{fmt.Errorf("projecting graph: %w", err)}
	}

	runID = ulid.Make().String()
	now := time.Now().UTC()
	actor := req.Actor
	if actor == "" {
		actor = "trigger"
	}
	meta := eventstore.AppendMeta{Actor: actor, CorrelationID: runID, OccurredAt: now}

	trigger := domain.TriggerReceived{
		RunID:         runID,
		TriggerRef:    req.TriggerRef,
		DefinitionRef: absPath,
		Params:        req.Params,
		ParentRunID:   req.ParentRunID,
		CorrelationID: runID,
	}
	if _, err := store.AppendIf(ctx, runID, 0, []domain.Event{trigger}, meta); err != nil {
		return "", "", fmt.Errorf("appending trigger.received: %w", err)
	}

	state, _, err := domain.Advance(domain.RunState{}, trigger, now)
	if err != nil {
		return "", "", fmt.Errorf("folding trigger.received: %w", err)
	}
	started := domain.RunStarted{RunID: runID, Graph: graph}
	state, _, err = domain.Advance(state, started, now)
	if err != nil {
		return "", "", fmt.Errorf("folding run.started: %w", err)
	}
	if _, err := store.AppendIf(ctx, runID, 1, []domain.Event{started}, meta); err != nil {
		return "", "", fmt.Errorf("appending run.started: %w", err)
	}

	return runID, state.Status, nil
}

func checkQueueLimits(ctx context.Context, store eventstore.Store, limits QueueLimits) error {
	if limits.MaxQueued > 0 {
		runs, err := store.ListRuns(ctx, nil)
		if err != nil {
			return fmt.Errorf("checking run queue depth: %w", err)
		}
		nonTerminal := 0
		for _, r := range runs {
			if !domain.RunStatus(r.Status).Terminal() {
				nonTerminal++
			}
		}
		if nonTerminal >= limits.MaxQueued {
			return ErrQueueFull
		}
	}
	if limits.MaxOpenDecisions > 0 {
		n, err := countOpenHumanDecisions(ctx, store)
		if err != nil {
			return fmt.Errorf("checking open human decisions: %w", err)
		}
		if n >= limits.MaxOpenDecisions {
			return ErrTooManyOpenDecisions
		}
	}
	return nil
}

// countOpenHumanDecisions counts NodeExecutions in ExecWaiting whose
// node's static Wait.Kind is "human", across every non-terminal run —
// 08-triggers.md's "maxOpenDecisions: 5 stops starting work when five
// things already wait on you."
func countOpenHumanDecisions(ctx context.Context, store eventstore.Store) (int, error) {
	runs, err := store.ListRuns(ctx, nil)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, r := range runs {
		if domain.RunStatus(r.Status).Terminal() {
			continue
		}
		state, ok, err := store.GetRunState(ctx, r.RunID)
		if err != nil || !ok {
			continue
		}
		for nodeID, execs := range state.Executions {
			node, ok := state.Graph.NodeByID(nodeID)
			if !ok || node.Wait == nil || node.Wait.Kind != domain.WaitHuman {
				continue
			}
			for _, e := range execs {
				if e.Status == domain.ExecWaiting {
					count++
				}
			}
		}
	}
	return count, nil
}
