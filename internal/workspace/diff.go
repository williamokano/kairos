// Diff support for the web UI's diff viewer (L20-webui.md's Future work,
// built here): reads between two already-recorded points (a
// WorkspaceSnapshotTaken ref/SHA, or the project's configured BaseRef) —
// it takes no new snapshot and writes nothing. Same runGitOutputEnv
// plumbing SnapshotGitRef/RestoreGitRef already use, so this file adds no
// new git-invocation path, only new arguments to the existing one.
package workspace

import (
	"context"
)

// DiffPatch returns the raw unified diff (`git diff --no-color
// --unified=3 fromRef toRef`) between two refs/SHAs in ws's repository —
// the text the diff viewer parses into files and hunks.
func (m *Manager) DiffPatch(ctx context.Context, ws Workspace, fromRef, toRef string) (string, error) {
	return m.runGitOutputEnv(ctx, ws.Dir, nil, "diff", "--no-color", "--unified=3", fromRef, toRef)
}

// DiffNumstat returns `git diff --numstat fromRef toRef`'s output — one
// line per changed file: added lines, removed lines (or "-" for a binary
// file), then the path. This is the machine-readable summary the diff
// viewer's file list and scope-violation check are built from, read
// separately from DiffPatch's full text rather than parsed back out of
// it, matching internal/constraint/gitdiff.go's existing split between a
// stat call and a content call for the same reason: --numstat's shape is
// stable and easy to parse; extracting the same counts from unified-diff
// hunk headers is not.
func (m *Manager) DiffNumstat(ctx context.Context, ws Workspace, fromRef, toRef string) (string, error) {
	return m.runGitOutputEnv(ctx, ws.Dir, nil, "diff", "--numstat", fromRef, toRef)
}
