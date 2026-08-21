package registry_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/registry"
)

func TestSynthesizeAdHoc_producesAValidOneNodeDefinition(t *testing.T) {
	home := t.TempDir()
	path, err := registry.SynthesizeAdHoc(home, "say hello", registry.AdHocOptions{Actor: "claude"})
	if err != nil {
		t.Fatalf("SynthesizeAdHoc: %v", err)
	}
	if filepath.Dir(path) != filepath.Join(home, "adhoc") {
		t.Errorf("path = %s, want it under %s/adhoc", path, home)
	}

	def, err := registry.Load(path)
	if err != nil {
		t.Fatalf("Load(%s): %v", path, err)
	}
	if len(def.Nodes) != 1 {
		t.Fatalf("len(Nodes) = %d, want 1", len(def.Nodes))
	}
	nd := def.Nodes[0]
	if nd.Actor != "claude" {
		t.Errorf("Actor = %q, want claude", nd.Actor)
	}
	if nd.Prompt != "say hello" {
		t.Errorf("Prompt = %q, want the exact text", nd.Prompt)
	}
	if nd.Workspace != registry.WorkspaceRead {
		t.Errorf("Workspace = %q, want read", nd.Workspace)
	}
	if !nd.PostOutputToConversation {
		t.Error("PostOutputToConversation = false, want true")
	}
	if nd.OutputSchema == nil {
		t.Error("OutputSchema is nil, want a compiled permissive schema")
	}
	if nd.ResumeSessionID != "" {
		t.Errorf("ResumeSessionID = %q, want empty on a fresh (non-continuation) synthesis", nd.ResumeSessionID)
	}
}

func TestSynthesizeAdHoc_continuationCarriesResumeAndConversationTarget(t *testing.T) {
	home := t.TempDir()
	path, err := registry.SynthesizeAdHoc(home, "and then what", registry.AdHocOptions{
		Actor:                   "claude",
		ResumeSessionID:         "01a02568-f135-678b-fe6d-ae0af32a2a58",
		ConversationRunOverride: "01M0ORIGINALRUN",
	})
	if err != nil {
		t.Fatalf("SynthesizeAdHoc: %v", err)
	}
	def, err := registry.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	nd := def.Nodes[0]
	if nd.ResumeSessionID != "01a02568-f135-678b-fe6d-ae0af32a2a58" {
		t.Errorf("ResumeSessionID = %q, want the passed session id", nd.ResumeSessionID)
	}
	if nd.ConversationRunOverride != "01M0ORIGINALRUN" {
		t.Errorf("ConversationRunOverride = %q, want the original run id", nd.ConversationRunOverride)
	}
}

func TestSynthesizeAdHoc_requiresAnActor(t *testing.T) {
	if _, err := registry.SynthesizeAdHoc(t.TempDir(), "hi", registry.AdHocOptions{}); err == nil {
		t.Fatal("expected an error with no Actor set")
	}
}

// TestGC_removesOnlyInactiveAndOldFiles mirrors internal/workspace.GC's
// own test style (L06): a mix of terminal-and-old (removed),
// terminal-and-recent (kept — the retention window's whole point, so a
// just-finished chat turn stays forkable), and active/referenced (kept
// regardless of age) files, and confirms only the correct set goes.
func TestGC_removesOnlyInactiveAndOldFiles(t *testing.T) {
	home := t.TempDir()
	adhocDir := filepath.Join(home, "adhoc")
	if err := os.MkdirAll(adhocDir, 0o700); err != nil {
		t.Fatal(err)
	}

	oldInactive := filepath.Join(adhocDir, "old-inactive.yaml")
	recentInactive := filepath.Join(adhocDir, "recent-inactive.yaml")
	oldActive := filepath.Join(adhocDir, "old-active.yaml")
	for _, p := range []string{oldInactive, recentInactive, oldActive} {
		if err := os.WriteFile(p, []byte("name: x\nnodes: []\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now().UTC()
	old := now.Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(oldInactive, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(oldActive, old, old); err != nil {
		t.Fatal(err)
	}
	// recentInactive keeps its just-written mtime (~now).

	active := map[string]bool{oldActive: true}
	removed, err := registry.GC(home, active, 7*24*time.Hour, now)
	if err != nil {
		t.Fatalf("GC: %v", err)
	}

	if len(removed) != 1 || removed[0] != oldInactive {
		t.Errorf("removed = %v, want exactly [%s]", removed, oldInactive)
	}
	for _, p := range []string{recentInactive, oldActive} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s to survive GC, got: %v", p, err)
		}
	}
	if _, err := os.Stat(oldInactive); err == nil {
		t.Errorf("expected %s to be removed by GC", oldInactive)
	}
}
