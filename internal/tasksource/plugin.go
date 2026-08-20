package tasksource

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/eventstore"
	"github.com/williamokano/kairos/internal/executor/local"
)

// pluginRequest/pluginResponse mirror 08-triggers.md's NDJSON envelope
// exactly — {"v":1,"op":...,"callID":...,"plugin":...,"config":...,
// "input":...,"deadline":...} out, {"v":1,"callID":...,"ok":...,
// "output"|"error":...} back.
type pluginRequest struct {
	V        int             `json:"v"`
	Op       string          `json:"op"`
	CallID   string          `json:"callID"`
	Plugin   string          `json:"plugin"`
	Config   json.RawMessage `json:"config,omitempty"`
	Input    any             `json:"input,omitempty"`
	Deadline string          `json:"deadline,omitempty"`
}

type pluginResponse struct {
	V      int             `json:"v"`
	CallID string          `json:"callID"`
	OK     bool            `json:"ok"`
	Output json.RawMessage `json:"output,omitempty"`
	Error  *SourceError    `json:"error,omitempty"`
}

// Plugin is a Source backed by a stdio NDJSON executable — 08-triggers.md's
// "primary" extension mechanism: any language, `chmod +x` is the install,
// one process per call (this document implements one-shot invocation
// only; stream mode is named Future work in L16-triggers.md).
type Plugin struct {
	Name        string
	Path        string
	Config      json.RawMessage
	Secrets     map[string]string // declared secret name -> value
	ScratchRoot string            // WorkRoot/plugins/<name>/<callID>/
	Exec        local.Executor
	Store       eventstore.Store // for recording secret.accessed; nil disables the event
}

func (p *Plugin) Describe(ctx context.Context) (Descriptor, error) {
	out, err := p.call(ctx, "describe", nil)
	if err != nil {
		return Descriptor{}, err
	}
	var d Descriptor
	if err := json.Unmarshal(out, &d); err != nil {
		return Descriptor{}, &SourceError{Code: ErrInternal, Message: "decoding describe output: " + err.Error()}
	}
	return d, nil
}

func (p *Plugin) Poll(ctx context.Context, in PollInput) (PollOutput, error) {
	out, err := p.call(ctx, "poll", in)
	if err != nil {
		return PollOutput{}, err
	}
	var po PollOutput
	if err := json.Unmarshal(out, &po); err != nil {
		return PollOutput{}, &SourceError{Code: ErrInternal, Message: "decoding poll output: " + err.Error()}
	}
	return po, nil
}

func (p *Plugin) Ack(ctx context.Context, in AckInput) (AckOutput, error) {
	_, err := p.call(ctx, "ack", in)
	if err != nil {
		return AckOutput{}, err
	}
	return AckOutput{}, nil
}

// call is one full request/response cycle: spawn the plugin (through the
// sole execution chokepoint, internal/executor/local), write the request
// to stdin, wait for exit, and parse stdout as the response — never the
// process's ambient PATH/env beyond what Env explicitly grants (secrets
// arrive as KAIROS_SECRET_<NAME>, never in the request body, so the
// request itself is safe to record in the event log verbatim).
func (p *Plugin) call(ctx context.Context, op string, input any) (json.RawMessage, error) {
	callID := "c_" + ulid.Make().String()
	req := pluginRequest{
		V: 1, Op: op, CallID: callID, Plugin: p.Name, Config: p.Config, Input: input,
		Deadline: time.Now().Add(30 * time.Second).UTC().Format(time.RFC3339),
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, &SourceError{Code: ErrInternal, Message: "encoding request: " + err.Error()}
	}

	dir := filepath.Join(p.ScratchRoot, callID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, &SourceError{Code: ErrInternal, Message: "creating plugin scratch dir: " + err.Error()}
	}

	env := []string{"HOME=" + dir, "PATH=/usr/bin:/bin:/usr/local/bin"}
	for name, value := range p.Secrets {
		env = append(env, "KAIROS_SECRET_"+strings.ToUpper(name)+"="+value)
		if p.Store != nil {
			_ = recordSecretAccessed(ctx, p.Store, p.Name, name, callID)
		}
	}

	started, err := p.Exec.Start(ctx, local.ExecSpec{
		RunID: "tasksource", NodeID: p.Name, ExecID: callID,
		Dir: dir, WorkDir: dir, Env: env, Argv: []string{p.Path}, Stdin: body,
	})
	if err != nil {
		return nil, &SourceError{Code: ErrInternal, Message: "starting plugin: " + err.Error()}
	}
	res, err := p.Exec.Wait(ctx, started.PID)
	if err != nil {
		return nil, &SourceError{Code: ErrInternal, Message: "waiting for plugin: " + err.Error()}
	}

	resp, parseErr := readPluginResponse(dir)
	if parseErr != nil {
		// Non-zero exit with no parseable JSON normalises to a typed
		// internal error carrying the last 2KiB of stderr, per the doc.
		stderr := lastNBytes(filepath.Join(dir, "stderr.log"), 2048)
		return nil, &SourceError{Code: ErrInternal, Message: fmt.Sprintf("exit %d: %s", res.ExitCode, stderr)}
	}
	if !resp.OK {
		if resp.Error == nil {
			return nil, &SourceError{Code: ErrInternal, Message: "plugin reported failure with no error detail"}
		}
		if !IsClosedErrorCode(resp.Error.Code) {
			return nil, &SourceError{Code: ErrInternal, Message: "plugin returned non-contract error code " + resp.Error.Code}
		}
		return nil, resp.Error
	}
	return resp.Output, nil
}

func readPluginResponse(dir string) (pluginResponse, error) {
	f, err := os.Open(filepath.Join(dir, "stdout.log"))
	if err != nil {
		return pluginResponse{}, err
	}
	defer func() { _ = f.Close() }()

	var last string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			last = line
		}
	}
	if last == "" {
		return pluginResponse{}, fmt.Errorf("no output")
	}
	var resp pluginResponse
	if err := json.Unmarshal([]byte(last), &resp); err != nil {
		return pluginResponse{}, err
	}
	return resp, nil
}

func lastNBytes(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return string(b)
}

func recordSecretAccessed(ctx context.Context, store eventstore.Store, plugin, secret, callID string) error {
	return appendSystemEvent(ctx, store, domain.SecretAccessed{Plugin: plugin, Secret: secret, CallID: callID})
}

// appendSystemEvent appends one event to the "system" stream — the same
// pattern internal/engine's Engine.appendSystem established in L05,
// duplicated here (not imported) since internal/engine must not import
// internal/tasksource (TestArchitecture_runCreationNotReachableFromActors)
// and this package must not import internal/engine either, to keep that
// boundary a two-way fact rather than one this package could quietly
// route around.
func appendSystemEvent(ctx context.Context, store eventstore.Store, ev domain.Event) error {
	envs, err := store.Read(ctx, eventstore.SystemStream)
	if err != nil {
		return fmt.Errorf("reading system stream: %w", err)
	}
	_, err = store.AppendIf(ctx, eventstore.SystemStream, len(envs), []domain.Event{ev}, eventstore.AppendMeta{
		Actor: "tasksource", CorrelationID: eventstore.SystemStream, OccurredAt: time.Now().UTC(),
	})
	return err
}
