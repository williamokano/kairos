package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/executor/local"
	"github.com/williamokano/kairos/internal/registry"
)

// dispatchShellActor maps actor: "shell" to `/bin/sh -c <prompt>` for the
// milestone only (decision #4) — not the real, probed command-shape
// resolution 04-agents.md describes for LLM CLIs (L08's job). The
// contract a shell node's command must honour: exit 0 and write valid
// JSON to $KAIROS_OUTPUT_PATH to succeed; anything else fails or, if the
// file is missing/invalid, is treated as schema-invalid output.
func (e *Engine) dispatchShellActor(ctx context.Context, nd registry.NodeDef, c domain.CmdStartNode) error {
	// The admission claim for c.ExecID is released here on every
	// synchronous failure path (releaseAndDrain is idempotent-safe if
	// called twice), and by reapShell once the process actually exits —
	// spawned marks the point past which the reaper, not this function,
	// owns that release.
	spawned := false
	defer func() {
		if !spawned {
			e.releaseAndDrain(ctx, c.ExecID)
		}
	}()

	dir := e.scratchDir(c.RunID, c.ExecID)
	outputPath := filepath.Join(dir, "output.json")

	// workspace: write nodes run inside a real, provisioned git clone
	// (ADR 0005 via internal/workspace) instead of the bare scratch dir —
	// L05's decision #7 deferred this to L06. Nodes that don't declare
	// workspace: write are unaffected: their cwd stays the per-exec
	// scratch dir, unchanged from L05.
	workDir := dir
	if nd.Workspace == registry.WorkspaceWrite {
		if e.workspaceRepo == "" {
			return e.appendNodeFailed(ctx, c.RunID, c.NodeID, c.ExecID, domain.FailFailure,
				"node declares workspace: write but the engine has no configured WorkspaceRepo")
		}
		ws, err := e.workspaces.Provision(ctx, c.RunID, e.workspaceRepo)
		if err != nil {
			return e.appendNodeFailed(ctx, c.RunID, c.NodeID, c.ExecID, domain.FailFailure, "provisioning workspace: "+err.Error())
		}
		workDir = ws.Dir
	}

	started, err := e.exec.Start(ctx, local.ExecSpec{
		RunID: c.RunID, NodeID: c.NodeID, ExecID: c.ExecID,
		Dir:     dir,
		WorkDir: workDir,
		Env: []string{
			"HOME=" + dir,
			"PATH=/usr/bin:/bin:/usr/local/bin",
			"KAIROS_OUTPUT_PATH=" + outputPath,
			"KAIROS_RUN_ID=" + c.RunID,
			"KAIROS_NODE_ID=" + c.NodeID,
			"KAIROS_EXEC_ID=" + c.ExecID,
			// KAIROS_RUN_DIR is stable across retries/attempts (unlike
			// Dir, which is per-exec) — a node that needs to guard its
			// own idempotency across attempts (12-build-plan.md's n3
			// ledger probe) writes its guard file here.
			"KAIROS_RUN_DIR=" + e.scratchDir(c.RunID, ""),
		},
		Argv: []string{"/bin/sh", "-c", nd.Prompt},
	})
	if err != nil {
		return e.appendNodeFailed(ctx, c.RunID, c.NodeID, c.ExecID, domain.FailFailure, "starting process: "+err.Error())
	}

	if err := e.appendNext(ctx, c.RunID, domain.NodeExecutionStarted(c)); err != nil {
		return err
	}

	spawned = true
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.reapShell(ctx, nd, c.RunID, c.NodeID, c.ExecID, started.PID, outputPath)
	}()
	return nil
}

// reapShell blocks until the process exits, then turns its outcome into
// the appropriate fact event, validating output.json against the node's
// declared OutputSchema — SchemaValid means "conforms," not merely
// "parses."
func (e *Engine) reapShell(ctx context.Context, nd registry.NodeDef, runID, nodeID, execID string, pid int, outputPath string) {
	defer e.releaseAndDrain(ctx, execID)

	res, err := e.exec.Wait(ctx, pid)
	if err != nil {
		_ = e.appendNodeFailed(ctx, runID, nodeID, execID, domain.FailFailure, "waiting for process: "+err.Error())
		return
	}
	if res.ExitCode != 0 {
		_ = e.appendNodeFailed(ctx, runID, nodeID, execID, domain.FailFailure, fmt.Sprintf("exit status %d", res.ExitCode))
		return
	}

	body, err := os.ReadFile(outputPath)
	if err != nil {
		_ = e.appendNext(ctx, runID, domain.NodeOutputReceived{
			RunID: runID, NodeID: nodeID, ExecID: execID, SchemaValid: false,
		})
		return
	}

	valid := validateOutput(nd, body)
	_ = e.appendNext(ctx, runID, domain.NodeOutputReceived{
		RunID: runID, NodeID: nodeID, ExecID: execID, SchemaValid: valid, Output: json.RawMessage(body),
	})
}

func validateOutput(nd registry.NodeDef, body []byte) bool {
	decoded, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		return false
	}
	if nd.OutputSchema == nil {
		return true
	}
	return nd.OutputSchema.Validate(decoded) == nil
}
