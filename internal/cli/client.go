package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Client is the daemon API client — an ordinary net/http.Client dialing a
// unix socket. It has no knowledge of internal/eventstore or
// internal/domain's storage shapes; response bodies are decoded into this
// package's own small mirror types.
type Client struct {
	http *http.Client
	base string
}

// NewClient builds a Client dialing sockPath for every request. base is
// always "http://kairos" — the host is unused (the unix socket dial
// ignores it) but net/http requires a well-formed URL.
func NewClient(sockPath string) *Client {
	return &Client{
		http: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					d := net.Dialer{}
					return d.DialContext(ctx, "unix", sockPath)
				},
			},
			Timeout: 30 * time.Second,
		},
		base: "http://kairos",
	}
}

// APIError is returned when the daemon responds with a non-2xx status; it
// carries the status code so CLI verbs can map it to an exit code.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s (status %d): %s", e.Code, e.Status, e.Message)
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshalling request: %w", err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("connecting to daemon: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		var apiErr struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&apiErr)
		return &APIError{Status: resp.StatusCode, Code: apiErr.Error.Code, Message: apiErr.Error.Message}
	}

	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}

// CreateRunResponse mirrors internal/api's createRunResponse.
type CreateRunResponse struct {
	RunID  string `json:"runId"`
	Status string `json:"status"`
}

func (c *Client) CreateRun(ctx context.Context, definitionPath string, params json.RawMessage) (CreateRunResponse, error) {
	var out CreateRunResponse
	err := c.do(ctx, http.MethodPost, "/runs", map[string]any{
		"definitionPath": definitionPath,
		"params":         params,
	}, &out)
	return out, err
}

// RunSummary mirrors internal/eventstore.RunSummary's JSON shape.
type RunSummary struct {
	RunID     string `json:"RunID"`
	Status    string `json:"Status"`
	StartedAt string `json:"StartedAt"`
	UpdatedAt string `json:"UpdatedAt"`
}

func (c *Client) ListRuns(ctx context.Context, status string) ([]RunSummary, error) {
	path := "/runs"
	if status != "" {
		path += "?status=" + status
	}
	var out struct {
		Runs []RunSummary `json:"runs"`
	}
	err := c.do(ctx, http.MethodGet, path, nil, &out)
	return out.Runs, err
}

// RunState mirrors the fields of domain.RunState this CLI cares about
// rendering — a local shape rather than importing internal/domain's
// storage-oriented type.
type RunState struct {
	ID         string                     `json:"ID"`
	Status     string                     `json:"Status"`
	Executions map[string][]NodeExecution `json:"Executions"`
}

type NodeExecution struct {
	ExecID    string `json:"ExecID"`
	NodeID    string `json:"NodeID"`
	Status    string `json:"Status"`
	Attempt   int    `json:"Attempt"`
	Iteration int    `json:"Iteration"`
}

func (c *Client) GetRun(ctx context.Context, id string) (RunState, error) {
	var out RunState
	err := c.do(ctx, http.MethodGet, "/runs/"+id, nil, &out)
	return out, err
}

type StatusResponse struct {
	DaemonPID  int    `json:"daemonPid"`
	Uptime     string `json:"uptime"`
	ActiveRuns int    `json:"activeRuns"`
	Paused     bool   `json:"paused"`
	InFlight   int    `json:"inFlight"`
}

func (c *Client) Status(ctx context.Context) (StatusResponse, error) {
	var out StatusResponse
	err := c.do(ctx, http.MethodGet, "/status", nil, &out)
	return out, err
}

type DoctorCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type DoctorResponse struct {
	Checks   []DoctorCheck `json:"checks"`
	Deferred []string      `json:"deferred"`
}

func (c *Client) Doctor(ctx context.Context) (DoctorResponse, error) {
	var out DoctorResponse
	err := c.do(ctx, http.MethodGet, "/doctor", nil, &out)
	return out, err
}

type DBVerifyResponse struct {
	MismatchedRunIDs []string `json:"mismatchedRunIds"`
}

func (c *Client) DBVerify(ctx context.Context) (DBVerifyResponse, error) {
	var out DBVerifyResponse
	err := c.do(ctx, http.MethodPost, "/db/verify", nil, &out)
	return out, err
}

func (c *Client) DBRebuild(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/db/rebuild", nil, nil)
}

func (c *Client) DBBackup(ctx context.Context, path string) error {
	return c.do(ctx, http.MethodPost, "/db/backup", map[string]string{"path": path}, nil)
}

func (c *Client) Pause(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/pause", nil, nil)
}

func (c *Client) Resume(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/resume", nil, nil)
}

// SelfCheckReport mirrors internal/engine.SelfCheckReport's wire shape —
// hand-mirrored, not imported, matching this file's existing convention
// (see ConversationMessage's doc comment).
type SelfCheckReport struct {
	DBClean                 bool     `json:"DBClean"`
	MismatchedRunIDs        []string `json:"MismatchedRunIDs"`
	UnverifiableExecutions  []string `json:"UnverifiableExecutions"`
	OrphanWorkspacesRemoved []string `json:"OrphanWorkspacesRemoved"`
}

func (c *Client) SelfCheck(ctx context.Context) (SelfCheckReport, error) {
	var out SelfCheckReport
	err := c.do(ctx, http.MethodPost, "/selfcheck", nil, &out)
	return out, err
}

// ConversationMessage mirrors internal/domain.ConversationMessageAppended
// — a separate type rather than importing internal/domain, matching this
// file's existing pattern (RunState/NodeExecution above are hand-mirrored
// too, not imported): internal/cli stays independent of the engine's own
// packages, consulting only the wire shape over the socket.
type ConversationMessage struct {
	Role string `json:"Role"`
	Text string `json:"Text"`
}

type conversationResponse struct {
	Messages []ConversationMessage `json:"messages"`
}

func (c *Client) GetConversation(ctx context.Context, runID string) ([]ConversationMessage, error) {
	var out conversationResponse
	err := c.do(ctx, http.MethodGet, "/runs/"+runID+"/conversation", nil, &out)
	return out.Messages, err
}

func (c *Client) PostConversationMessage(ctx context.Context, runID, text string) error {
	return c.do(ctx, http.MethodPost, "/runs/"+runID+"/conversation/messages", map[string]string{"text": text}, nil)
}

// ApproveHumanTask posts a kairos-approve-shaped answer. reason is
// required by the daemon side (engine.ErrHumanDecisionReasonRequired) —
// this method does not pre-validate it locally, so the error surfaces
// from the same place regardless of caller (API test or CLI).
func (c *Client) ApproveHumanTask(ctx context.Context, runID, nodeID, decision, reason, typedWord string) error {
	return c.do(ctx, http.MethodPost, "/runs/"+runID+"/approve", map[string]string{
		"nodeId": nodeID, "decision": decision, "reason": reason, "typedWord": typedWord,
	}, nil)
}

// EffectSummary mirrors internal/api's effectSummaryResponse.
type EffectSummary struct {
	NodeID                  string `json:"nodeId"`
	ExecID                  string `json:"execId"`
	Effect                  string `json:"effect"`
	Outcome                 string `json:"outcome"`
	ExternalRef             string `json:"externalRef,omitempty"`
	Reason                  string `json:"reason,omitempty"`
	Compensated             bool   `json:"compensated"`
	WouldCompensateOnCancel bool   `json:"wouldCompensateOnCancel"`
}

// Effects lists a run's recorded effect actions — `kairos effects <run>`.
func (c *Client) Effects(ctx context.Context, runID string) ([]EffectSummary, error) {
	var out []EffectSummary
	err := c.do(ctx, http.MethodGet, "/runs/"+runID+"/effects", nil, &out)
	return out, err
}

// ResolveEffect answers a node blocked in effect.unknown — `kairos
// effects resolve`. reason is required by the daemon side.
func (c *Client) ResolveEffect(ctx context.Context, runID, nodeID, outcome, reason string) error {
	return c.do(ctx, http.MethodPost, "/runs/"+runID+"/effects/resolve", map[string]string{
		"nodeId": nodeID, "outcome": outcome, "reason": reason,
	}, nil)
}

// ForkResult mirrors internal/api's forkResponse.
type ForkResult struct {
	NewRunID          string `json:"newRunId"`
	Drifted           bool   `json:"drifted"`
	ActualSnapshotSeq int    `json:"actualSnapshotSeq"`
}

// Fork posts a kairos-fork-shaped request. overrides may be nil.
func (c *Client) Fork(ctx context.Context, runID string, atSequence int, overrides map[string]string, allowDrift bool) (ForkResult, error) {
	var out ForkResult
	err := c.do(ctx, http.MethodPost, "/runs/"+runID+"/fork", map[string]any{
		"atSequence": atSequence, "overrides": overrides, "allowDrift": allowDrift,
	}, &out)
	return out, err
}

// CompareSide mirrors internal/api's runSummaryForCompare.
type CompareSide struct {
	RunID       string `json:"runId"`
	Status      string `json:"status"`
	Duration    int64  `json:"durationNanos"`
	Attempts    int    `json:"attempts"`
	Findings    int    `json:"findings"`
	ForkedFrom  string `json:"forkedFrom,omitempty"`
	Drifted     bool   `json:"drifted"`
	DriftDetail string `json:"driftDetail,omitempty"`
}

type CompareResult struct {
	A CompareSide `json:"a"`
	B CompareSide `json:"b"`
}

func (c *Client) Compare(ctx context.Context, runA, runB string) (CompareResult, error) {
	var out CompareResult
	err := c.do(ctx, http.MethodGet, "/runs/"+runA+"/compare/"+runB, nil, &out)
	return out, err
}

// Ping reports whether the daemon responds at all — used by ensureDaemon
// to detect a live socket before attempting auto-start.
func (c *Client) Ping(ctx context.Context) bool {
	_, err := c.Status(ctx)
	return err == nil
}

// Source mirrors internal/api's sourceResponse — backs `kairos src ls`.
type Source struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	Flow            string `json:"flow,omitempty"`
	Project         string `json:"project,omitempty"`
	IntervalSeconds int    `json:"intervalSeconds"`
	Enabled         bool   `json:"enabled"`
	Health          string `json:"health"`
	HealthReason    string `json:"healthReason,omitempty"`
}

func (c *Client) AddSource(ctx context.Context, id, kind, config, flow, project string, intervalSeconds int) (Source, error) {
	var out Source
	err := c.do(ctx, http.MethodPost, "/sources", map[string]any{
		"id": id, "kind": kind, "config": config, "flow": flow, "project": project, "intervalSeconds": intervalSeconds,
	}, &out)
	return out, err
}

func (c *Client) ListSources(ctx context.Context) ([]Source, error) {
	var out struct {
		Sources []Source `json:"sources"`
	}
	err := c.do(ctx, http.MethodGet, "/sources", nil, &out)
	return out.Sources, err
}

func (c *Client) PauseSource(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/sources/"+id+"/pause", nil, nil)
}

func (c *Client) ResumeSource(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/sources/"+id+"/resume", nil, nil)
}

// Envelope mirrors the fields internal/events.Envelope serializes over
// /events — the daemon's canonical event log, read here rather than
// reimplemented anywhere else (internal/tui's decision/inbox screens
// derive risk/gates/findings from this, since no summarized "decision
// context" endpoint exists yet — L15-tui.md's Documented decisions).
type Envelope struct {
	StreamID  string          `json:"StreamID"`
	Sequence  int             `json:"Sequence"`
	GlobalSeq int64           `json:"GlobalSeq"`
	EventType string          `json:"EventType"`
	Event     json.RawMessage `json:"Event"`
}

// Events fetches every historical envelope currently in streamID (or every
// stream, if empty) via GET /events's non-streaming replay portion. It
// stops reading once the server goes quiet for readTimeout — the SSE
// endpoint stays open forever for live tailing, and this call wants only
// the bounded history, matching the pattern cmd/kairos's own integration
// tests already use against this same endpoint.
func (c *Client) Events(ctx context.Context, streamID string, readTimeout time.Duration) ([]Envelope, error) {
	path := "/events?after=0"
	if streamID != "" {
		path = "/events?stream=" + streamID + "&after=0"
	}
	// A deadline of its own, distinct from ctx: the connection stays open
	// forever for live tailing, so "the deadline expired" is the expected,
	// successful end of a bounded historical read here, not an error.
	readCtx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(readCtx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connecting to daemon: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return nil, &APIError{Status: resp.StatusCode, Message: "GET /events failed"}
	}

	var envs []Envelope
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var env Envelope
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &env); err == nil {
			envs = append(envs, env)
		}
	}
	if err := scanner.Err(); err != nil && readCtx.Err() == nil {
		return envs, fmt.Errorf("reading events: %w", err)
	}
	return envs, nil
}
