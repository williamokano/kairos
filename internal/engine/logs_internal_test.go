package engine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/williamokano/kairos/internal/artifact"
	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/events"
	"github.com/williamokano/kairos/internal/eventstore"
	"github.com/williamokano/kairos/internal/executor/local"
)

// White-box tests (package engine, not engine_test) for storeOutput and
// collectLogs — both unexported, and both cheapest to exercise directly
// rather than through a full subprocess round-trip.

func openTestStore(t *testing.T) eventstore.Store {
	t.Helper()
	registry, err := events.Builtin()
	if err != nil {
		t.Fatalf("Builtin: %v", err)
	}
	st, err := eventstore.Open(context.Background(), eventstore.Config{
		Path:     filepath.Join(t.TempDir(), "kairos.db"),
		Registry: registry,
		Projections: []eventstore.Projection{
			eventstore.RunStateProjection{},
			eventstore.RunIndexProjection{},
		},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func newMinimalEngine(t *testing.T, st eventstore.Store, workRoot string) *Engine {
	t.Helper()
	return New(Config{
		Store:    st,
		Executor: local.New(local.DefaultBootIDProvider()),
		BootID:   local.DefaultBootIDProvider(),
		WorkRoot: workRoot,
	})
}

func TestStoreOutput_smallBodyStaysInline(t *testing.T) {
	e := newMinimalEngine(t, openTestStore(t), t.TempDir())

	body := []byte(`{"ok":true}`)
	inline, ref, err := e.storeOutput(body)
	if err != nil {
		t.Fatalf("storeOutput: %v", err)
	}
	if ref != nil {
		t.Errorf("expected no artifact ref for a small body, got %+v", ref)
	}
	if !bytes.Equal(inline, body) {
		t.Errorf("inline = %q, want %q", inline, body)
	}
}

func TestStoreOutput_oversizedBodyBecomesAnArtifactReference(t *testing.T) {
	e := newMinimalEngine(t, openTestStore(t), t.TempDir())

	body := append([]byte(`{"big":"`), bytes.Repeat([]byte("x"), inlineThreshold)...)
	body = append(body, []byte(`"}`)...)

	inline, ref, err := e.storeOutput(body)
	if err != nil {
		t.Fatalf("storeOutput: %v", err)
	}
	if inline != nil {
		t.Errorf("expected no inline body for an oversized body, got %d bytes", len(inline))
	}
	if ref == nil {
		t.Fatal("expected an artifact ref for an oversized body")
	}
	stored, err := os.ReadFile(e.artifacts.Path(artifact.Ref{Hash: ref.Hash, Size: ref.Size}))
	if err != nil {
		t.Fatalf("reading stored blob: %v", err)
	}
	if !bytes.Equal(stored, body) {
		t.Errorf("stored blob does not match the original body (got %d bytes, want %d)", len(stored), len(body))
	}
}

func TestCollectLogs_rotationFailureRecordsLogDegraded(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: permission-based failure injection doesn't apply")
	}
	st := openTestStore(t)
	e := newMinimalEngine(t, st, t.TempDir())
	ctx := context.Background()

	runID := "run_logs"
	meta := eventstore.AppendMeta{Actor: "test", CorrelationID: runID}
	if _, err := st.AppendIf(ctx, runID, 0, []domain.Event{
		domain.TriggerReceived{RunID: runID, TriggerRef: "t", DefinitionRef: "d", CorrelationID: runID},
	}, meta); err != nil {
		t.Fatalf("seeding trigger: %v", err)
	}

	dir := t.TempDir()
	oversized := bytes.Repeat([]byte("log line\n"), (65*1024*1024/9)+1)
	logPath := filepath.Join(dir, "stdout.log")
	if err := os.WriteFile(logPath, oversized, 0o600); err != nil {
		t.Fatalf("writing oversized log: %v", err)
	}
	// A read-only directory makes CollectLog's temp-file creation fail
	// deterministically (permission denied), simulating the "cannot
	// rotate safely" case 06-durability.md's backpressure policy covers,
	// without needing a real full disk.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	e.collectLogs(ctx, runID, "n1", "n1#a1.i1", dir)

	envs, err := st.Read(ctx, runID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	found := false
	for _, env := range envs {
		if ld, ok := env.Event.(domain.LogDegraded); ok {
			found = true
			if ld.Stream != "stdout" {
				t.Errorf("LogDegraded.Stream = %q, want stdout", ld.Stream)
			}
			if ld.Reason == "" {
				t.Error("expected a non-empty Reason")
			}
		}
	}
	if !found {
		t.Error("expected a log.degraded event to be recorded, found none")
	}
	// The original log must survive untouched — a failed rotation must
	// never leave a silent gap.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("original log missing after a failed rotation: %v", err)
	}
	if !bytes.Equal(got, oversized) {
		t.Error("original log content changed after a failed rotation")
	}
}
