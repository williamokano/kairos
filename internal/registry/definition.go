package registry

import (
	"encoding/json"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/williamokano/kairos/internal/domain"
)

// Definition is a fully parsed, defaulted, and validated workflow — the
// rich shape every later layer (L05 engine, L08 actors, L10 gates) reads
// directly, keyed by TriggerReceived.DefinitionRef. domain.Graph, by
// contrast, carries only the routing-relevant subset — see ProjectGraph.
type Definition struct {
	Name         string
	SourcePath   string
	ParamsSchema *jsonschema.Schema // compiled from the params: shorthand
	Nodes        []NodeDef
	Limits       LimitsDef
	// Gates is the workflow's own gate library, keyed by ID: a top-level
	// `gates:` map in the same YAML document, resolved against by every
	// node's Gates []string list (declared order). L10-constraints-gates.md
	// documents this as a deliberately narrower stand-in for 05-gates.md's
	// full constitution.yaml resolution/merge system (kairos/baseline +
	// project + repo tiers) — that belongs to L11 (policy).
	Gates map[string]GateDef
}

// GateKind is one of 05-gates.md's ten kinds. L10 implemented expr and
// command; L11 adds file, regex, git-diff, coverage, and judged — the
// three non-code domain kinds (grounded/recipients/outbound-scan) stay
// Future work, specified in 13-domains.md and out of this document's
// scope (see L11-policy-secrets.md's Documented decisions).
type GateKind string

const (
	GateExpr     GateKind = "expr"
	GateCommand  GateKind = "command"
	GateFile     GateKind = "file"
	GateRegex    GateKind = "regex"
	GateGitDiff  GateKind = "git-diff"
	GateCoverage GateKind = "coverage"
	GateJudged   GateKind = "judged"
)

// GateDef is one `gates:` entry — a node references it by ID via its own
// Gates []string. Fields not relevant to Kind are simply unset; there is
// no separate per-kind struct because the two kinds share so little
// shape that a sum type would cost more in indirection than it buys.
type GateDef struct {
	ID       string
	Kind     GateKind
	Severity string
	Message  string
	// Waivable defaults true; only an explicit `waivable: false` sets it
	// false. There is no waiver-grant mechanism yet (L11's job per
	// 05-gates.md's "waiver.grant is deny-tier for every non-human
	// principal" — a human-authored waiver event) — until it exists,
	// Waivable is enforced as an unconditional invariant: false means
	// "no code path in this engine can mark this gate's failure as
	// passed," full stop.
	Waivable bool

	// Expr is the expr-lang/expr expression, present when Kind == GateExpr.
	// ADR 0013 documents the library choice and its syntax divergence
	// from 05-gates.md's literal JSONPath examples.
	Expr string

	// Command fields, present when Kind == GateCommand. Also reused by
	// GateCoverage (Command is the coverage-run command; see CoverageThen).
	Command        []string
	Workdir        string        // relative to the node's workspace; absolute paths rejected at validate time
	ExpectExitCode int           // defaults to 0
	Timeout        time.Duration // 0 means no timeout
	FindingsFormat string        // e.g. "golangci-json"; empty means no findings adapter

	// File fields, present when Kind == GateFile.
	FileExists []string // glob patterns, rooted at the workspace, that must each match at least one file
	FileAbsent []string // glob patterns that must match nothing

	// Regex fields, present when Kind == GateRegex.
	RegexOver    string   // "added-lines" is the only supported value in this document's scope (see Documented decisions)
	RegexAbsent  string   // a match on any selected line fails the gate
	RegexExclude []string // glob patterns; files matching any are skipped

	// GitDiff fields, present when Kind == GateGitDiff.
	GitDiffPathsForbidden []string
	GitDiffMustTouch      []string
	GitDiffMaxFiles       int // 0 means unbounded
	GitDiffMaxLines       int // 0 means unbounded
	GitDiffNoBinary       bool
	GitDiffDirty          *bool // non-nil: assert working-tree dirty state equals *GitDiffDirty
	GitDiffStaged         *bool // non-nil: assert staged state equals *GitDiffStaged

	// Coverage fields, present when Kind == GateCoverage. Command (above)
	// runs first; CoverageThen runs second and its stdout is where
	// CoverageCaptureRegex is applied. Baseline-vs-base-ref comparison
	// (05-gates.md's `baseline: git`) is Future work — see
	// L11-policy-secrets.md's Documented decisions.
	CoverageThen         []string
	CoverageCaptureRegex string
	CoverageMin          float64

	// Judged fields, present when Kind == GateJudged.
	JudgeActors   []string // the "from" list; JudgeQuorumOf of these must agree
	JudgeQuorumOf int
	JudgeLens     string
	// JudgeFraming is always "refutation" in this document's scope — the
	// only framing 05-gates.md's own example uses and the only one that
	// carries the "default to inconclusive" property the doc calls "the
	// whole trick." Parsed and validated so a workflow author who writes
	// something else gets a publish error, not silent reinterpretation.
	JudgeFraming string
}

// NodeDef is one node's full authored shape.
type NodeDef struct {
	ID     domain.NodeID
	Actor  string
	Prompt string

	Inputs       map[string]InputRef
	OutputSchema *jsonschema.Schema // always compiled and non-nil after Load
	// OutputSchemaRaw is the same schema as OutputSchema, undecoded — an
	// llm actor's $KAIROS_SCHEMA file is this written verbatim, and
	// `kairos check-output` validates against a file, not an in-memory
	// *jsonschema.Schema, so the raw document has to survive alongside
	// the compiled one (L08; a *jsonschema.Schema doesn't marshal back to
	// its source document).
	OutputSchemaRaw json.RawMessage

	Context        []string
	Workspace      WorkspaceMode
	WorkspacePaths []string
	HostExclusive  bool

	Resources       ResourcesDef
	Timeout         time.Duration
	SessionAffinity string

	Retry RetryDef

	// Gates and Effects are carried as opaque names: L10 resolves gate
	// names against a gate library, L12 resolves effect names against the
	// effect manager. Neither is validated here beyond "non-empty string".
	Gates   []string
	Effects []string
	// With is actor: effect's static argument block — see yamlNode.With's
	// doc comment (parse.go) for the exact shape and its Future-work
	// limitation (static values only, no dynamic input-binding yet).
	With map[string]string

	Artifacts []ArtifactSpec

	// On, unlike domain.WaitSpec's single Kind, preserves every wait
	// source the author declared (poll + webhook, say); ProjectGraph
	// collapses it to the first entry's Kind for domain.Node (decision
	// #4 in L03-definition-validator.md).
	Wait *WaitDef

	Spawn *SpawnDef
	Join  *JoinDef

	Optional bool

	// On carries any author-declared edge overrides
	// (success/failure/timeout/rejected/denied -> node ID), consulted by
	// resolveEdges before it falls back to the document-order default.
	On map[domain.EdgeTrigger]domain.NodeID

	// SideEffectFree is the author's declaration that re-running this
	// node from scratch is safe — it drives RestartPolicy's default
	// (12-build-plan.md: "restartPolicy: rerun ... default when
	// sideEffectFree: true", "fail-to-human ... default when
	// sideEffectFree is unset or false"). Restart policy is a
	// dispatch-time engine concern read off the Definition by
	// DefinitionRef, not a domain.Advance routing concern — it lives
	// here, not on domain.Node.
	SideEffectFree bool
	// RestartPolicy is resolved from SideEffectFree (or an explicit
	// author override) at defaulting time — see RestartPolicy's doc.
	RestartPolicy RestartPolicy

	// PostOutputToConversation, ConversationRunOverride, and
	// ResumeSessionID back `kairos do`'s ad hoc chat feature — see
	// SynthesizeAdHoc's doc comment (adhoc.go). PostOutputToConversation:
	// when a node's real output arrives, also append it as an
	// "assistant" message to a Conversation stream (its own run's by
	// default, or ConversationRunOverride's if set — continuing a chat
	// spins up a NEW run per turn whose reply should land in the
	// ORIGINAL run's thread, not its own). ResumeSessionID, when
	// non-empty, is used directly as the LLM actor's native --resume
	// target, bypassing resolveSession's normal same-run/same-node prior-
	// attempt derivation (which cannot span runs) — see
	// engine.resolveSession's doc comment for the exact branch.
	PostOutputToConversation bool
	ConversationRunOverride  string
	ResumeSessionID          string
	// WorkDirOverride, when non-empty, is used verbatim as the LLM
	// actor's real working directory instead of the normal per-exec
	// scratch dir or a workspace: write reference-clone — a `kairos
	// session`'s own directory (its Project's git worktree, or its bare
	// path if the Project isn't git-backed; see ADR 0014 and
	// internal/project). The per-exec scratch dir is still used for
	// output.json/schema/logs and HOME's credential isolation — only the
	// process's WorkDir changes (internal/executor/local.ExecSpec
	// already separates Dir from WorkDir for exactly this reason).
	WorkDirOverride string
}

// RestartPolicy names how the engine treats a NodeExecution the log says
// was Executing when the daemon restarts (12-build-plan.md).
type RestartPolicy string

const (
	// RestartRerun re-dispatches the node from scratch. Default when
	// SideEffectFree is true; safe because re-running costs nothing
	// side-effect-wise by the author's own declaration.
	RestartRerun RestartPolicy = "rerun"
	// RestartFailToHuman parks the node (ExecParked{ParkNonIdempotentAtBoot})
	// rather than guessing. Default when SideEffectFree is unset or false.
	RestartFailToHuman RestartPolicy = "fail-to-human"
	// RestartAdopt re-attaches to a surviving process instead of killing
	// it: internal/engine/reconcile.go's VerdictAlive branch leaves an
	// adopted process running and watches it for its real exit
	// (12-build-plan.md: "adopt in L06, once the reconciliation loop it
	// plugs into is proven" — see L06-workspaces.md).
	RestartAdopt RestartPolicy = "adopt"
)

// WorkspaceMode mirrors 03-workflows.md's workspace: none | read | write.
type WorkspaceMode string

const (
	WorkspaceNone  WorkspaceMode = "none"
	WorkspaceRead  WorkspaceMode = "read"
	WorkspaceWrite WorkspaceMode = "write"
)

// InputRef is one `inputs:` entry: either the bare JSONPath shorthand
// ("$.outputs.plan.tasks") or the long form ({path, default}).
type InputRef struct {
	Path    string
	Default *[]byte // nil means no default; a present-but-null default is []byte("null")
}

// ResourcesDef mirrors 03-workflows.md's resources: { model: {...} }.
type ResourcesDef struct {
	ModelClass string
	ModelSlots int
	MaxCostUSD float64
}

// RetryDef is the author-facing retry policy. MaxAttempts feeds
// domain.RetryPolicy directly; RetryOn/FreshWorkspace/Mutate are read by
// later layers (L05/L06), not by domain.
type RetryDef struct {
	MaxAttempts    int
	RetryOn        []string
	FreshWorkspace bool
	Mutate         []MutateStep
}

// MutateStep is one `retry.mutate` entry.
type MutateStep struct {
	Attempt    int
	Actor      string
	ModelClass string
}

// ArtifactSpec mirrors 03-workflows.md's artifacts: entries.
type ArtifactSpec struct {
	Name   string
	From   string
	Kind   string
	Always bool
}

// WaitKind mirrors 03-workflows.md's wait.on[].kind values.
type WaitKind string

const (
	WaitHuman        WaitKind = "human"
	WaitTimer        WaitKind = "timer"
	WaitPoll         WaitKind = "poll"
	WaitChildRun     WaitKind = "child-run"
	WaitConversation WaitKind = "conversation"
	WaitWebhook      WaitKind = "webhook"
)

// WaitSource is one entry in wait.on[].
type WaitSource struct {
	Kind     WaitKind
	Command  []string // poll
	Every    time.Duration
	Until    string
	Endpoint string // webhook
	Match    string // webhook
}

// WaitDef is the author-facing wait declaration. OnTimeout is required
// whenever Wait is non-nil (03-workflows.md: "REQUIRED FIELD").
type WaitDef struct {
	On        []WaitSource
	Timeout   time.Duration // relative; ProjectGraph does not resolve this to an absolute instant (decision #3)
	OnTimeout string        // "escalate" | "park"
	// Weight is 05-gates.md's "decision weight must match reversibility"
	// tier — "silent" | "glance" | "read" | "type" — meaningful only for
	// a wait.on kind: human entry (L13-human-decisions.md). Empty means
	// unset; defaultWait resolves it to WeightRead, the doc's middle
	// friction level, when Kind == human and no weight was declared.
	Weight string
}

// Human-decision weight values 05-gates.md's table names, in ascending
// order of friction. A node's Weight is a declared policy property, never
// actor-chosen (see internal/engine's AnswerHumanTask, which enforces
// this as a real invariant, not just documentation).
const (
	WeightSilent = "silent"
	WeightGlance = "glance"
	WeightRead   = "read"
	WeightType   = "type"
)

// SpawnDef mirrors 03-workflows.md's spawn: block. Parsed and structurally
// validated only — dispatch is L17.
type SpawnDef struct {
	Workflow         string
	ForEach          string
	Strategy         string // e.g. "bounded(3)"
	InheritWorkspace string
}

// JoinDef mirrors 03-workflows.md's join: block.
type JoinDef struct {
	Mode           string // e.g. "waitAll"
	OnChildFailure string // e.g. "degrade"
}

// LimitsDef mirrors 03-workflows.md's limits: block, workflow-scoped.
type LimitsDef struct {
	WallClock         time.Duration
	MaxCostUSD        float64
	MaxNodeExecutions int
	MaxSpawnDepth     int
	LoopGuard         LoopGuardDef
}

// LoopGuardDef mirrors 03-workflows.md's limits.loopGuard block.
type LoopGuardDef struct {
	MaxIterationsPerNode int
	OnExceeded           string // e.g. "escalate-to-human"
}
