package tasksource

import (
	"context"
	"encoding/json"
	"time"
)

// WorkItem is one unit of upstream work a Source's Poll returns.
// DedupeKey, Project, Body, and Budget are 08-triggers.md's fields —
// DedupeKey is required (empty is a contract violation, checked by
// ValidateWorkItem); Project, Body, Budget are new relative to the
// original design because locally work happens in a directory that
// already exists on disk and a prose task is often the entire payload.
type WorkItem struct {
	ID        string            `json:"id"`
	DedupeKey string            `json:"dedupeKey"`
	Title     string            `json:"title"`
	Body      string            `json:"body,omitempty"`
	Project   string            `json:"project,omitempty"`
	Flow      string            `json:"flow,omitempty"`
	Params    json.RawMessage   `json:"params,omitempty"`
	Priority  int               `json:"priority,omitempty"`
	Budget    float64           `json:"budget,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// ValidateWorkItem enforces the one required field the doc calls out by
// name: a WorkItem with no DedupeKey cannot be deduplicated, which is the
// entire point of routing anything through this package rather than
// calling CreateRun directly.
func ValidateWorkItem(w WorkItem) error {
	if w.DedupeKey == "" {
		return &SourceError{Code: ErrBadRequest, Message: "work item " + w.ID + " has no dedupeKey"}
	}
	return nil
}

// Descriptor is a Source's static self-description — the `describe` op.
type Descriptor struct {
	Name            string          `json:"name"`
	Kinds           []string        `json:"kinds"`
	Ops             []string        `json:"ops"`
	Secrets         []string        `json:"secrets,omitempty"`
	DefaultInterval string          `json:"defaultInterval,omitempty"`
	ConfigSchema    json.RawMessage `json:"configSchema,omitempty"`
}

// PollInput/PollOutput are the `poll` op's request/response.
type PollInput struct {
	Cursor string `json:"cursor,omitempty"`
	// ETag is the previous poll's response ETag, for a Source to issue a
	// conditional request (`If-None-Match`) — "a 304 costs no quota at
	// all" (08-triggers.md).
	ETag  string `json:"etag,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type PollOutput struct {
	Items  []WorkItem `json:"items"`
	Cursor string     `json:"cursor"`
	// ETag is round-tripped back into the next call's PollInput.ETag via
	// source_cursor — owned by the daemon, never the plugin.
	ETag      string        `json:"etag,omitempty"`
	PollAfter time.Duration `json:"pollAfter,omitempty"`
}

// AckInput/AckOutput are the `ack` op's request/response — routed
// through the effect-style idempotency path in ack.go, never called
// directly against a Source from poller/inbox code.
type AckInput struct {
	ItemID         string `json:"itemID"`
	DedupeKey      string `json:"dedupeKey"`
	Outcome        string `json:"outcome"` // triggered | succeeded | failed | rejected
	RunID          string `json:"runID,omitempty"`
	ResultURL      string `json:"resultURL,omitempty"`
	Summary        string `json:"summary,omitempty"`
	Reason         string `json:"reason,omitempty"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type AckOutput struct{}

// Source is the TaskSource contract (08-triggers.md): three operations,
// `watch` folds into stream mode rather than being a fourth (stream mode
// itself is Future work — see plugin.go's doc comment).
type Source interface {
	Describe(ctx context.Context) (Descriptor, error)
	Poll(ctx context.Context, in PollInput) (PollOutput, error)
	Ack(ctx context.Context, in AckInput) (AckOutput, error)
}

// The closed error-code set 08-triggers.md specifies. Any other code is
// itself a contract violation, not a new kind of failure.
const (
	ErrBadRequest    = "bad_request"
	ErrUnauthorized  = "unauthorized"
	ErrNotFound      = "not_found"
	ErrUnsupportedOp = "unsupported_op"
	ErrRateLimited   = "rate_limited"
	ErrUpstream      = "upstream"
	ErrInternal      = "internal"
)

var closedErrorCodes = map[string]bool{
	ErrBadRequest: true, ErrUnauthorized: true, ErrNotFound: true, ErrUnsupportedOp: true,
	ErrRateLimited: true, ErrUpstream: true, ErrInternal: true,
}

// IsClosedErrorCode reports whether code is one of the seven the contract
// permits — a Source (builtin, plugin, or fake) that returns anything
// else has violated the contract, not merely failed.
func IsClosedErrorCode(code string) bool { return closedErrorCodes[code] }

// SourceError is a Source operation's typed failure.
type SourceError struct {
	Code       string
	Message    string
	Retryable  bool
	RetryAfter time.Duration
}

func (e *SourceError) Error() string { return e.Code + ": " + e.Message }
