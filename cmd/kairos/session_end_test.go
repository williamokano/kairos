package main_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestKairosSessionEnd_removesTheRealWorktree is `kairos session end`'s
// end-to-end proof: a real git-backed Project's Session gets a real
// `git worktree`, and ending it actually removes that worktree from disk
// and from `git worktree list` — the concrete, tested meaning of
// internal/project.Manager.EndSession finally being reachable through the
// daemon API instead of existing only as an internal method nothing
// called.
func TestKairosSessionEnd_removesTheRealWorktree(t *testing.T) {
	bin := buildKairos(t)
	home := t.TempDir()
	repo := t.TempDir()
	_ = gitInit(t, repo)

	h := newDaemonHarness(t, bin, home)
	h.start(t, 5*time.Second)
	h.waitForReconciled(t, 3*time.Second)

	client := h.httpClient()

	// Create the Project.
	projBody, _ := json.Marshal(map[string]string{"name": "e2e-proj", "repoPath": repo})
	resp, err := client.Post("http://kairos/projects", "application/json", bytes.NewReader(projBody))
	if err != nil {
		t.Fatalf("POST /projects: %v", err)
	}
	var proj struct{ ID string }
	if err := json.NewDecoder(resp.Body).Decode(&proj); err != nil {
		t.Fatalf("decoding project: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /projects: status %d", resp.StatusCode)
	}

	// Start a Session bound to it — a real worktree gets provisioned.
	sessBody, _ := json.Marshal(map[string]string{"projectId": proj.ID, "actor": "claude"})
	resp, err = client.Post("http://kairos/sessions", "application/json", bytes.NewReader(sessBody))
	if err != nil {
		t.Fatalf("POST /sessions: %v", err)
	}
	var sess struct{ ID, WorkDir string }
	if err := json.NewDecoder(resp.Body).Decode(&sess); err != nil {
		t.Fatalf("decoding session: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /sessions: status %d", resp.StatusCode)
	}
	if _, err := os.Stat(sess.WorkDir); err != nil {
		t.Fatalf("session worktree does not exist: %v", err)
	}

	// A bypass attempt (wrong confirm) must be rejected AND must not
	// touch the worktree.
	badReq, _ := http.NewRequest(http.MethodDelete, "http://kairos/sessions/"+sess.ID,
		strings.NewReader(`{"reason":"cleanup","confirm":"not-the-id"}`))
	badResp, err := client.Do(badReq)
	if err != nil {
		t.Fatalf("DELETE (bad confirm): %v", err)
	}
	_ = badResp.Body.Close()
	if badResp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("bad-confirm DELETE status = %d, want 422", badResp.StatusCode)
	}
	if _, err := os.Stat(sess.WorkDir); err != nil {
		t.Fatalf("worktree must survive a bypass attempt: %v", err)
	}

	// The real end, with the matching confirm.
	goodReq, _ := http.NewRequest(http.MethodDelete, "http://kairos/sessions/"+sess.ID,
		strings.NewReader(`{"reason":"done with this chat","confirm":"`+sess.ID+`"}`))
	goodResp, err := client.Do(goodReq)
	if err != nil {
		t.Fatalf("DELETE (matching confirm): %v", err)
	}
	_ = goodResp.Body.Close()
	if goodResp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE status = %d, want 200", goodResp.StatusCode)
	}

	if _, err := os.Stat(sess.WorkDir); err == nil {
		t.Error("expected the worktree directory to be gone after ending the session")
	}
	out, err := exec.Command("git", "-C", repo, "worktree", "list").CombinedOutput()
	if err != nil {
		t.Fatalf("git worktree list: %v", err)
	}
	if strings.Contains(string(out), filepath.Base(sess.WorkDir)) {
		t.Errorf("git worktree list still shows the ended session's worktree:\n%s", out)
	}

	// The session record itself must be gone too — re-fetching it 404s.
	getResp, err := client.Get("http://kairos/sessions/" + sess.ID)
	if err != nil {
		t.Fatalf("GET ended session: %v", err)
	}
	_ = getResp.Body.Close()
	if getResp.StatusCode != http.StatusNotFound {
		t.Errorf("GET ended session status = %d, want 404", getResp.StatusCode)
	}

	if mismatches := h.dbVerify(t); len(mismatches) != 0 {
		t.Errorf("db verify found mismatches: %v", mismatches)
	}
}
