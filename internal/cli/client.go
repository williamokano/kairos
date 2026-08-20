package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
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

// Ping reports whether the daemon responds at all — used by ensureDaemon
// to detect a live socket before attempting auto-start.
func (c *Client) Ping(ctx context.Context) bool {
	_, err := c.Status(ctx)
	return err == nil
}
