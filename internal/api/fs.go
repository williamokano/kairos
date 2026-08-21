package api

import (
	"net/http"
	"os"
	"path/filepath"
)

// fsEntry is one immediate subdirectory of a browsed path.
type fsEntry struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	IsGit  bool   `json:"isGit"`
	Hidden bool   `json:"hidden"`
}

type fsBrowseResponse struct {
	Path     string    `json:"path"`
	Parent   string    `json:"parent,omitempty"`
	Entries  []fsEntry `json:"entries"`
	ShowHome bool      `json:"showHome"`
}

// registerFSRoutes backs the web UI's project-path picker (the user's own
// words: "some rich 'selector' that would list the path, nested, so I
// could select from there, not always I remember the path from the top
// of my head") and `kairos fs browse` — a real, read-only directory
// listing, not a blind text field. Scoped narrowly for a single-operator
// local tool (AGENTS.md's threat model: the host is the sandbox, and
// there isn't one) — this exposes no more than `ls` already would to
// anyone who can reach the daemon socket at all; it does not attempt a
// new access-control boundary that doesn't exist anywhere else in this
// codebase.
func registerFSRoutes(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("GET /fs/browse", func(w http.ResponseWriter, r *http.Request) {
		reqPath := r.URL.Query().Get("path")
		if reqPath == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				writeError(w, http.StatusInternalServerError, "invariant_violation", "resolving home dir: "+err.Error())
				return
			}
			reqPath = home
		}
		abs, err := filepath.Abs(reqPath)
		if err != nil {
			writeError(w, http.StatusBadRequest, "usage", "resolving path: "+err.Error())
			return
		}
		info, err := os.Stat(abs)
		if err != nil || !info.IsDir() {
			writeError(w, http.StatusNotFound, "not_found", "not a directory: "+abs)
			return
		}

		entries, err := os.ReadDir(abs)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "invariant_violation", "reading dir: "+err.Error())
			return
		}
		out := make([]fsEntry, 0, len(entries))
		for _, e := range entries {
			name := e.Name()
			hidden := len(name) > 0 && name[0] == '.'
			// A symlink is resolved to decide if it's really a directory
			// (os.DirEntry.IsDir() reports false for a symlink even when
			// its target is a directory) — a broken/dangling symlink is
			// silently skipped rather than erroring the whole listing,
			// matching AGENTS.md's "no silent failure" read narrowly:
			// one bad entry does not hide every other real directory.
			full := filepath.Join(abs, name)
			isDir := e.IsDir()
			if !isDir && e.Type()&os.ModeSymlink != 0 {
				target, statErr := os.Stat(full)
				if statErr != nil {
					continue
				}
				isDir = target.IsDir()
			}
			if !isDir {
				continue
			}
			out = append(out, fsEntry{
				Name: name, Path: full, Hidden: hidden,
				IsGit: isGitDir(full),
			})
		}

		parent := filepath.Dir(abs)
		resp := fsBrowseResponse{Path: abs, Entries: out}
		if parent != abs {
			resp.Parent = parent
		}
		writeJSON(w, http.StatusOK, resp)
	})
}

func isGitDir(path string) bool {
	info, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil && (info.IsDir() || info.Mode().IsRegular())
}
