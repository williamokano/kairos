package registry

import (
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
}

// NodeDef is one node's full authored shape.
type NodeDef struct {
	ID     domain.NodeID
	Actor  string
	Prompt string

	Inputs       map[string]InputRef
	OutputSchema *jsonschema.Schema // always compiled and non-nil after Load

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
}

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
