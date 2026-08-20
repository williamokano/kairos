package engine

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/williamokano/kairos/internal/admission"
	"github.com/williamokano/kairos/internal/artifact"
	"github.com/williamokano/kairos/internal/constraint"
	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/events"
	"github.com/williamokano/kairos/internal/eventstore"
	"github.com/williamokano/kairos/internal/executor/local"
	"github.com/williamokano/kairos/internal/policy"
	"github.com/williamokano/kairos/internal/workspace"
)

// Config assembles an Engine.
type Config struct {
	Store    eventstore.Store
	Executor local.Executor
	BootID   local.BootIDProvider
	// WorkRoot is where per-node-execution scratch directories are
	// created: WorkRoot/<runID>/<execID>/. A workspace: write node's cwd
	// is instead WorkRoot/<runID>/repo, a real git clone provisioned by
	// internal/workspace (see WorkspaceRepo).
	WorkRoot string
	// MirrorRoot is where internal/workspace keeps its per-repo bare
	// mirrors (ADR 0005). Defaults to a "mirrors" sibling of WorkRoot's
	// parent (i.e. $KAIROS_HOME/mirrors) if empty.
	MirrorRoot string
	// WorkspaceRepo is the git repository workspace: write nodes clone
	// from. Empty means no write-mode node can run — dispatchShellActor
	// fails that node loudly rather than silently falling back to a bare
	// scratch dir (AGENTS §4 rule 1). Per-run repo selection, sourced
	// from the trigger's invocation cwd, is Future work (see
	// L06-workspaces.md) — this document scopes to one daemon-wide repo.
	WorkspaceRepo string
	// ArtifactRoot is where internal/artifact keeps its content-addressed
	// blob store (L09). Defaults to an "artifacts" sibling of WorkRoot's
	// parent (i.e. $KAIROS_HOME/artifacts) if empty, matching MirrorRoot's
	// defaulting convention.
	ArtifactRoot string
	// LLMBinary is the CLI binary an llm-kind actor (claude/codex/gemini/
	// local) invokes — configured, never assumed installed (L08). Empty
	// means no llm-kind node can run; dispatchLLMActor fails that node
	// loudly rather than silently falling back to anything.
	LLMBinary string
	// KillGrace is the SIGTERM-to-SIGKILL grace period for CmdSignalNode.
	KillGrace time.Duration
	// NumShards is how many goroutines partition run processing by
	// hash(runID). Defaults to 4 if zero.
	NumShards int
	Logger    *slog.Logger

	// Admission sizes L07's admission pools (internal/admission). A
	// zero-value Config disables every capacity rule (unlimited) — see
	// admission.New's doc comment.
	Admission admission.Config

	// ConstitutionProjectPath is 05-gates.md's authoritative,
	// outside-every-workspace layer:
	// ~/.kairos/projects/<project>/constitution.yaml. Empty means no
	// project-level gate overrides — only kairos/baseline and a workflow's
	// own inline gates: apply.
	ConstitutionProjectPath string
	// Policy is 05-gates.md's effect-permission tiers
	// (~/.kairos/policy.yaml), consulted before a node with declared
	// Effects is dispatched. A zero-value Policy (no Effects map) resolves
	// via policy.Policy's own Decide default: unknown effect -> deny.
	Policy policy.Policy
	// BaseRef is the run's base ref for git-diff/regex(added-lines) gate
	// kinds (05-gates.md's `{{ .base }}` — never a hardcoded
	// "origin/main"). Empty means those gate kinds fail loudly rather
	// than guessing one. Per-run base-ref selection is Future work (see
	// L11-policy-secrets.md) — this is one daemon-wide default, matching
	// WorkspaceRepo's existing single-repo scope from L06.
	BaseRef string
}

// Engine is the advance loop: Store.Subscribe -> domain.Advance -> dispatch.
type Engine struct {
	store         eventstore.Store
	exec          local.Executor
	bootID        local.BootIDProvider
	workRoot      string
	workspaceRepo string
	workspaces    *workspace.Manager
	artifacts     *artifact.Store
	llmBinary     string
	killGrace     time.Duration
	numShards     int
	log           *slog.Logger

	constitutionProjectPath string
	policy                  policy.Policy
	baseRef                 string

	admit       *admission.Manager
	constraints *constraint.Evaluator
	// pendingMu guards pending — every enqueue/dequeue/drain happens under
	// it, since a Queued node execution can be re-tried from any shard
	// goroutine's dispatch call or from another node's release, never
	// exclusively the shard that queued it.
	pendingMu sync.Mutex
	pending   []pendingStart
	// drainMu serializes drainPending against itself across concurrent
	// releases — see admission.go's doc comment on why pendingMu alone
	// isn't enough to prevent a queued node being admitted twice.
	drainMu sync.Mutex
	// claimsMu guards claims — set at grant time (dispatchStartNode),
	// read and cleared at release time (the reap goroutines), which run on
	// a different goroutine than the shard that granted them.
	claimsMu sync.Mutex
	claims   map[string]admission.Claims // execID -> claims held for it

	shards []*shard

	cancel context.CancelFunc
	wg     sync.WaitGroup

	// runCtx is the live-loop context armTimer's time.AfterFunc callbacks
	// run under — set once by Start, read by armTimer/Reconcile's timer
	// re-arming. A CmdArmTimer dispatched before Start (from Reconcile's
	// own catch-up pass) uses ctx directly instead; see armTimer's doc
	// comment.
	runCtxMu sync.Mutex
	runCtx   context.Context

	// timersMu guards timers — an in-memory, NOT persisted wait-timeout
	// wheel (L05 decision #6 named the gap; this document fills it only
	// as far as "fires for real while the daemon is up," not "survives a
	// restart mid-countdown" — see L13-human-decisions.md's Documented
	// decisions for why a full persisted timer wheel stays out of scope).
	// Reconcile's catch-up pass is what makes an OVERDUE timeout correct
	// across a restart; a not-yet-due one is simply re-armed there.
	timersMu sync.Mutex
	timers   map[string]*time.Timer // key: runID+"/"+nodeID+"/"+execID
}

// pendingStart is one CmdStartNode admission Queued — everything
// dispatchStartNode needs to retry the exact same dispatch later, with no
// side effect yet recorded (admission runs before NodeExecutionStarted is
// ever appended, so replaying this is exactly as if dispatch were called
// for the first time).
type pendingStart struct {
	definitionRef string
	cmd           domain.CmdStartNode
}

// New constructs an Engine. Call Reconcile before Start.
func New(cfg Config) *Engine {
	if cfg.NumShards <= 0 {
		cfg.NumShards = 4
	}
	if cfg.KillGrace <= 0 {
		cfg.KillGrace = 10 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	mirrorRoot := cfg.MirrorRoot
	if mirrorRoot == "" {
		mirrorRoot = filepath.Join(filepath.Dir(cfg.WorkRoot), "mirrors")
	}
	artifactRoot := cfg.ArtifactRoot
	if artifactRoot == "" {
		artifactRoot = filepath.Join(filepath.Dir(cfg.WorkRoot), "artifacts")
	}
	e := &Engine{
		store:         cfg.Store,
		exec:          cfg.Executor,
		bootID:        cfg.BootID,
		workRoot:      cfg.WorkRoot,
		workspaceRepo: cfg.WorkspaceRepo,
		workspaces:    workspace.New(mirrorRoot, cfg.WorkRoot, cfg.Executor),
		artifacts:     artifact.New(artifactRoot),
		llmBinary:     cfg.LLMBinary,
		killGrace:     cfg.KillGrace,
		numShards:     cfg.NumShards,
		log:           cfg.Logger,
		admit:         admission.New(cfg.Admission),
		claims:        make(map[string]admission.Claims),
		timers:        make(map[string]*time.Timer),

		constitutionProjectPath: cfg.ConstitutionProjectPath,
		policy:                  cfg.Policy,
		baseRef:                 cfg.BaseRef,
	}
	e.constraints = constraint.New(cfg.Executor, e.admit).WithJudge(e)
	e.shards = make([]*shard, e.numShards)
	for i := range e.shards {
		e.shards[i] = newShard(e)
	}
	return e
}

func (e *Engine) shardFor(runID string) *shard {
	h := fnv.New32a()
	_, _ = h.Write([]byte(runID))
	return e.shards[h.Sum32()%uint32(e.numShards)]
}

// scratchDir is where one NodeExecution's proc.json/stdout.log/stderr.log/
// output.json live — WorkRoot/<runID>/<execID>/.
func (e *Engine) scratchDir(runID, execID string) string {
	return filepath.Join(e.workRoot, runID, execID)
}

// Start seeds every non-terminal run's current state into its shard (see
// doc.go: this snapshot must happen before subscribing, so the live loop
// never double-folds an event the projection already applied), then
// begins the live advance loop. It returns once the loop is running;
// cancel ctx or call Stop to shut down.
func (e *Engine) Start(ctx context.Context) error {
	if err := e.primeShards(ctx); err != nil {
		return fmt.Errorf("priming run state cache: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	e.cancel = cancel
	e.runCtxMu.Lock()
	e.runCtx = runCtx
	e.runCtxMu.Unlock()

	if err := e.rearmOutstandingTimers(ctx); err != nil {
		return fmt.Errorf("re-arming wait timers: %w", err)
	}

	for _, sh := range e.shards {
		e.wg.Add(1)
		go func(sh *shard) {
			defer e.wg.Done()
			sh.run(runCtx)
		}(sh)
	}

	ch, unsub := e.store.Subscribe(runCtx)
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer unsub()
		for {
			select {
			case <-runCtx.Done():
				return
			case env, ok := <-ch:
				if !ok {
					return
				}
				if env.StreamID == eventstore.SystemStream {
					continue
				}
				if runID, ok := eventstore.RunIDFromConversationStream(env.StreamID); ok {
					// Conversation streams never enqueue to a shard (no
					// RunState to fold into — see IsAuxStream's doc
					// comment); a message's only effect is possibly
					// resolving a wait: conversation node, handled
					// synchronously here since conversation traffic is
					// human-paced, not hot-path.
					if err := e.resolveConversationWait(runCtx, runID, false); err != nil {
						e.log.Error("resolving wait: conversation", "runID", runID, "error", err)
					}
					continue
				}
				e.shardFor(env.StreamID).enqueue(env)
			}
		}
	}()

	return nil
}

func (e *Engine) primeShards(ctx context.Context) error {
	runs, err := e.store.ListRuns(ctx, nil)
	if err != nil {
		return err
	}
	for _, r := range runs {
		if r.Status.Terminal() {
			continue
		}
		state, ok, err := e.store.GetRunState(ctx, r.RunID)
		if err != nil {
			return fmt.Errorf("loading state for %s: %w", r.RunID, err)
		}
		if !ok {
			continue
		}
		definitionRef, err := e.firstEventDefinitionRef(ctx, r.RunID)
		if err != nil {
			return fmt.Errorf("resolving definitionRef for %s: %w", r.RunID, err)
		}
		e.shardFor(r.RunID).prime(r.RunID, state, definitionRef)
	}
	return nil
}

// firstEventDefinitionRef reads a run's TriggerReceived (always its first
// event, per L15) to recover DefinitionRef — domain.RunState itself
// doesn't carry it (see shard.go's defRefs doc comment).
func (e *Engine) firstEventDefinitionRef(ctx context.Context, runID string) (string, error) {
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
	return trigger.DefinitionRef, nil
}

// Stop cancels the live advance loop, waits for it to drain, and records
// EngineStopped — the fact the NEXT boot's unclean-exit check looks for.
//
// The incoming ctx bounds how long shutdown waits for graceful node
// interruption: interruptExecuting's Cancel call selects on it while
// waiting out killGrace, so it is routinely already expired by the time
// wg.Wait returns (a caller's shutdown deadline is typically shorter than
// killGrace, by design — shutdown must not hang forever on a stuck
// process). Recording EngineStopped must not inherit that exhausted
// budget, since it is the fact the next boot's unclean-exit detection
// depends on; it gets its own.
func (e *Engine) Stop(ctx context.Context) error {
	e.admit.SetDraining(true)
	if e.cancel != nil {
		e.cancel()
	}
	e.wg.Wait()
	e.stopAllTimers()

	recordCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := e.appendSystem(recordCtx, domain.EngineStopped{}); err != nil {
		return fmt.Errorf("recording engine.stopped: %w", err)
	}
	return nil
}

func (e *Engine) appendSystem(ctx context.Context, ev domain.Event) ([]events.Envelope, error) {
	seq, err := e.currentSeq(ctx, eventstore.SystemStream)
	if err != nil {
		return nil, err
	}
	return e.store.AppendIf(ctx, eventstore.SystemStream, seq, []domain.Event{ev}, eventstore.AppendMeta{
		Actor:         "engine",
		CorrelationID: eventstore.SystemStream,
		OccurredAt:    time.Now().UTC(),
	})
}

func (e *Engine) currentSeq(ctx context.Context, streamID string) (int, error) {
	envs, err := e.store.Read(ctx, streamID)
	if err != nil {
		return 0, fmt.Errorf("reading stream %s: %w", streamID, err)
	}
	return len(envs), nil
}
