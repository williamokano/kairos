package artifact_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/williamokano/kairos/internal/artifact"
)

func TestCollectLog_belowThresholdLeavesFileUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stdout.log")
	if err := os.WriteFile(path, []byte("small log"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	rotated, err := artifact.CollectLog(path)
	if err != nil {
		t.Fatalf("CollectLog: %v", err)
	}
	if rotated {
		t.Error("expected no rotation below the threshold")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("original log should still exist: %v", err)
	}
}

func TestCollectLog_missingFileIsANoOp(t *testing.T) {
	dir := t.TempDir()
	rotated, err := artifact.CollectLog(filepath.Join(dir, "does-not-exist.log"))
	if err != nil {
		t.Fatalf("CollectLog on a missing file should not error: %v", err)
	}
	if rotated {
		t.Error("expected no rotation for a missing file")
	}
}

func TestCollectLog_aboveThresholdRotatesAndCompresses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stdout.log")

	content := bytes.Repeat([]byte("kairos log line\n"), (artifact.RotateThreshold/16)+1024)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	rotated, err := artifact.CollectLog(path)
	if err != nil {
		t.Fatalf("CollectLog: %v", err)
	}
	if !rotated {
		t.Fatal("expected rotation above the threshold")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("original log should have been removed after compression, stat err = %v", err)
	}

	compressedPath := path + ".zst"
	compressed, err := os.Open(compressedPath)
	if err != nil {
		t.Fatalf("opening compressed log: %v", err)
	}
	defer func() { _ = compressed.Close() }()

	dec, err := zstd.NewReader(compressed)
	if err != nil {
		t.Fatalf("creating zstd reader: %v", err)
	}
	defer dec.Close()

	got, err := io.ReadAll(dec)
	if err != nil {
		t.Fatalf("decompressing: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("decompressed content does not match original (got %d bytes, want %d)", len(got), len(content))
	}
}
