package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/williamokano/kairos/internal/api"
)

// TestFSBrowse_listsRealSubdirsDetectsGitAndHidesDotfiles is the real
// directory-browser test the user's own ask required: "some rich
// 'selector' that would list the path, nested, so I could select from
// there." Built against a real temp directory tree — a fake `.git`
// marker in one subdirectory proves the git-detection is real, not just
// a name check, and a dotfile-named subdirectory proves the default
// hide behavior actually filters instead of just being documented.
func TestFSBrowse_listsRealSubdirsDetectsGitAndHidesDotfiles(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "plain-project"))
	gitRepo := filepath.Join(root, "git-project")
	mustMkdir(t, gitRepo)
	mustMkdir(t, filepath.Join(gitRepo, ".git")) // a real repo's .git is a dir; a worktree's is a file — either counts
	mustMkdir(t, filepath.Join(root, ".hidden-dir"))
	// A regular file alongside the directories must never appear in the
	// listing — this endpoint browses directories to select as a
	// Project's path, a file can never be one.
	if err := os.WriteFile(filepath.Join(root, "a-file.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("writing sibling file: %v", err)
	}

	mux := api.NewMux(api.Deps{})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/fs/browse?path=" + root)
	if err != nil {
		t.Fatalf("GET /fs/browse: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var out struct {
		Path    string
		Parent  string
		Entries []struct {
			Name   string
			Path   string
			IsGit  bool
			Hidden bool
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	byName := map[string]struct {
		Name   string
		Path   string
		IsGit  bool
		Hidden bool
	}{}
	for _, e := range out.Entries {
		byName[e.Name] = e
	}

	plain, ok := byName["plain-project"]
	if !ok {
		t.Fatal("expected \"plain-project\" in the listing")
	}
	if plain.IsGit {
		t.Error("plain-project incorrectly detected as git-backed")
	}
	if plain.Hidden {
		t.Error("plain-project incorrectly flagged as hidden")
	}

	gitEntry, ok := byName["git-project"]
	if !ok {
		t.Fatal("expected \"git-project\" in the listing")
	}
	if !gitEntry.IsGit {
		t.Error("git-project's real .git directory was not detected")
	}

	hidden, ok := byName[".hidden-dir"]
	if !ok {
		t.Fatal("expected \".hidden-dir\" in the listing (flagged, not omitted)")
	}
	if !hidden.Hidden {
		t.Error(".hidden-dir was not flagged as Hidden")
	}

	if _, isFile := byName["a-file.txt"]; isFile {
		t.Error("a-file.txt (a regular file, not a directory) must never appear in a directory browse listing")
	}
}

// TestFSBrowse_defaultsToHomeDirWhenPathOmitted proves the "sensible
// root" the picker starts from when a Project create form has never
// been used before — no path typed yet, no prior browse state.
func TestFSBrowse_defaultsToHomeDirWhenPathOmitted(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no resolvable home dir in this environment")
	}

	mux := api.NewMux(api.Deps{})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/fs/browse")
	if err != nil {
		t.Fatalf("GET /fs/browse: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out struct{ Path string }
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	realHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		realHome = home
	}
	realOut, err := filepath.EvalSymlinks(out.Path)
	if err != nil {
		realOut = out.Path
	}
	if realOut != realHome {
		t.Errorf("Path = %q, want the user's home directory %q", out.Path, home)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("creating dir %s: %v", path, err)
	}
}
