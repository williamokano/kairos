package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/williamokano/kairos/internal/domain"
)

// ErrNoWorkspaceSnapshot is returned by Diff when NodeID names a node that
// never took a workspace.snapshot.taken event — either it isn't a
// workspace: write node, or it hasn't completed a dispatch yet.
var ErrNoWorkspaceSnapshot = errors.New("engine: diff: no workspace snapshot recorded for this node")

// DiffRequest asks Diff for the file-level change one run (or one node
// within it) produced — L20-webui.md's Future work item, the diff viewer:
// L06's workspace snapshots (WorkspaceSnapshotTaken, ADR 0006) and L18's
// fork machinery already record every fact this needs; Diff is the first
// reader of that record other than Fork itself.
type DiffRequest struct {
	RunID string
	// NodeID, if set, scopes the diff to one node's own before/after
	// boundary (the workspace state just before it ran, to the snapshot
	// it recorded on completion). Empty means the whole run: the
	// project's configured BaseRef to the run's latest snapshot.
	NodeID string
}

// DiffFile is one changed file's numstat summary.
type DiffFile struct {
	Path           string
	Added, Removed int
	Binary         bool
}

// DiffResult is Diff's output: enough to render the diff viewer's file
// list, hunks, and scope-violation banner without a second daemon round
// trip.
type DiffResult struct {
	RunID, NodeID   string
	FromRef, ToRef  string
	Files           []DiffFile
	Patch           string // raw multi-file unified diff, --unified=3
	WorkspacePaths  []string
	ScopeViolations []string // Files' paths outside WorkspacePaths, only computed when WorkspacePaths is non-empty
}

// Diff computes req's file-level change by resolving two git refs — a
// prior WorkspaceSnapshotTaken (or the project's BaseRef, at the
// beginning of the sequence) and a later one — then running a real `git
// diff` between them via internal/workspace, the one place that runs git
// through the executor. Nothing here spawns a process itself.
func (e *Engine) Diff(ctx context.Context, req DiffRequest) (DiffResult, error) {
	envs, err := e.store.Read(ctx, req.RunID)
	if err != nil {
		return DiffResult{}, fmt.Errorf("engine: diff: reading run %s: %w", req.RunID, err)
	}
	if len(envs) == 0 {
		return DiffResult{}, fmt.Errorf("engine: diff: run %s has no events", req.RunID)
	}
	trigger, ok := envs[0].Event.(domain.TriggerReceived)
	if !ok {
		return DiffResult{}, fmt.Errorf("engine: diff: run %s's first event is not TriggerReceived", req.RunID)
	}

	type snap struct {
		nodeID, sha string
	}
	var snaps []snap
	for _, env := range envs {
		if s, ok := env.Event.(domain.WorkspaceSnapshotTaken); ok {
			snaps = append(snaps, snap{nodeID: s.NodeID, sha: s.SHA})
		}
	}

	result := DiffResult{RunID: req.RunID, NodeID: req.NodeID}
	var fromRef, toRef string
	switch req.NodeID {
	case "":
		if e.baseRef == "" {
			return DiffResult{}, fmt.Errorf("engine: diff: run %s: no base ref configured for this project (KAIROS_BASE_REF)", req.RunID)
		}
		fromRef = e.baseRef
		if len(snaps) == 0 {
			toRef = e.baseRef // no workspace: write node has completed yet — an honest empty diff, not an error
		} else {
			toRef = snaps[len(snaps)-1].sha
		}
	default:
		idx := -1
		for i, s := range snaps {
			if s.nodeID == req.NodeID {
				idx = i
			}
		}
		if idx == -1 {
			return DiffResult{}, fmt.Errorf("%w: node %s, run %s", ErrNoWorkspaceSnapshot, req.NodeID, req.RunID)
		}
		toRef = snaps[idx].sha
		switch {
		case idx > 0:
			fromRef = snaps[idx-1].sha
		case e.baseRef != "":
			fromRef = e.baseRef
		default:
			return DiffResult{}, fmt.Errorf("engine: diff: node %s is the run's first workspace write and no base ref is configured", req.NodeID)
		}
		if nd, ok := e.resolveNode(trigger.DefinitionRef, req.NodeID); ok {
			result.WorkspacePaths = nd.WorkspacePaths
		}
	}
	result.FromRef, result.ToRef = fromRef, toRef

	ws := e.workspaces.Existing(req.RunID)
	patch, err := e.workspaces.DiffPatch(ctx, ws, fromRef, toRef)
	if err != nil {
		return DiffResult{}, fmt.Errorf("engine: diff: computing patch: %w", err)
	}
	result.Patch = patch

	numstat, err := e.workspaces.DiffNumstat(ctx, ws, fromRef, toRef)
	if err != nil {
		return DiffResult{}, fmt.Errorf("engine: diff: computing numstat: %w", err)
	}
	result.Files = parseNumstat(numstat)

	if len(result.WorkspacePaths) > 0 {
		for _, f := range result.Files {
			if !anyScopeMatch(result.WorkspacePaths, f.Path) {
				result.ScopeViolations = append(result.ScopeViolations, f.Path)
			}
		}
	}
	return result, nil
}

// parseNumstat parses `git diff --numstat`'s tab-separated
// "added\tremoved\tpath" lines. A binary file reports "-" for both counts
// (git's own convention) rather than a number.
func parseNumstat(s string) []DiffFile {
	var out []DiffFile
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		f := DiffFile{Path: fields[2]}
		if fields[0] == "-" || fields[1] == "-" {
			f.Binary = true
		} else {
			f.Added, _ = strconv.Atoi(fields[0])
			f.Removed, _ = strconv.Atoi(fields[1])
		}
		out = append(out, f)
	}
	return out
}

// anyScopeMatch reports whether path matches at least one workspacePaths
// glob — the same `**`-prefix/suffix subset internal/constraint's
// gitdiff.go pathMatches implements for the identical git-diff-scope
// need (05-gates.md's pathsForbidden/mustTouch), duplicated in miniature
// here rather than exported across a package boundary for one ~10-line
// helper neither package otherwise depends on the other for.
func anyScopeMatch(patterns []string, path string) bool {
	for _, p := range patterns {
		if scopeMatch(p, path) {
			return true
		}
	}
	return false
}

func scopeMatch(pattern, path string) bool {
	if matched, _ := filepath.Match(pattern, path); matched {
		return true
	}
	if prefix, ok := strings.CutSuffix(pattern, "/**"); ok {
		if strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	if suffix, ok := strings.CutPrefix(pattern, "**/"); ok {
		if matched, _ := filepath.Match(suffix, filepath.Base(path)); matched {
			return true
		}
	}
	return false
}
