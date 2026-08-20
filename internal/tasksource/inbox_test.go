package tasksource_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/eventstore"
	"github.com/williamokano/kairos/internal/tasksource"
)

func TestInbox_oneFileProducesExactlyOneRun(t *testing.T) {
	st := openStore(t)
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = tasksource.RunInbox(ctx, tasksource.InboxConfig{
			Dir: dir, DefaultFlow: demoFlow(t), QuietPeriod: 50 * time.Millisecond, PollFallback: 100 * time.Millisecond,
		}, st)
	}()
	waitForInboxReady(t, dir)

	content := "---\nflow: " + demoFlow(t) + "\n---\nAdd cursor pagination.\n"
	if err := os.WriteFile(filepath.Join(dir, "task.md"), []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	waitForRuns(t, st, 1, 3*time.Second)

	takenDir := filepath.Join(dir, ".taken")
	entries, err := os.ReadDir(takenDir)
	if err != nil {
		t.Fatalf("ReadDir .taken: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("len(.taken entries) = %d, want 1 (the file was picked up)", len(entries))
	}
}

func TestInbox_rapidRewritesWithinQuietPeriodCollapseToOnePickup(t *testing.T) {
	st := openStore(t)
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = tasksource.RunInbox(ctx, tasksource.InboxConfig{
			Dir: dir, DefaultFlow: demoFlow(t), QuietPeriod: 200 * time.Millisecond, PollFallback: 500 * time.Millisecond,
		}, st)
	}()
	waitForInboxReady(t, dir)

	path := filepath.Join(dir, "task.md")
	for i := 0; i < 5; i++ {
		_ = os.WriteFile(path, []byte("---\nflow: "+demoFlow(t)+"\n---\nversion "+string(rune('a'+i))+"\n"), 0o600)
		time.Sleep(50 * time.Millisecond) // well inside the 200ms quiet period
	}

	waitForRuns(t, st, 1, 3*time.Second)
	// Confirm no MORE runs show up after settling — a second Test-visible
	// tick would mean the quiet-period debounce is not actually
	// collapsing the rewrites.
	time.Sleep(500 * time.Millisecond)
	runs, err := st.ListRuns(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("len(runs) = %d, want exactly 1 (rewrites collapsed)", len(runs))
	}
}

func TestInbox_identicalContentDroppedTwiceIsANoOp(t *testing.T) {
	st := openStore(t)
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = tasksource.RunInbox(ctx, tasksource.InboxConfig{
			Dir: dir, DefaultFlow: demoFlow(t), QuietPeriod: 50 * time.Millisecond, PollFallback: 100 * time.Millisecond,
		}, st)
	}()
	waitForInboxReady(t, dir)

	content := []byte("---\nflow: " + demoFlow(t) + "\n---\nsame task every time\n")
	if err := os.WriteFile(filepath.Join(dir, "a.md"), content, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	waitForRuns(t, st, 1, 3*time.Second)

	if err := os.WriteFile(filepath.Join(dir, "b.md"), content, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	runs, err := st.ListRuns(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("len(runs) = %d, want exactly 1 (identical content deduped)", len(runs))
	}
	dupDir := filepath.Join(dir, ".dup")
	entries, err := os.ReadDir(dupDir)
	if err != nil {
		t.Fatalf("ReadDir .dup: %v", err)
	}
	if len(entries) == 0 {
		t.Error("expected the duplicate to land in .dup/")
	}
}

func waitForInboxReady(t *testing.T, dir string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, ".taken")); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("inbox watcher did not initialise its subdirectories in time")
}

func waitForRuns(t *testing.T, st eventstore.Store, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		runs, err := st.ListRuns(context.Background(), nil)
		if err == nil && len(runs) >= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d run(s) to be created", n)
}
