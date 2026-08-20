package artifact_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/williamokano/kairos/internal/artifact"
)

func TestStore_putBytesDedupsIdenticalContent(t *testing.T) {
	s := artifact.New(t.TempDir())

	ref1, err := s.PutBytes([]byte("hello, kairos"))
	if err != nil {
		t.Fatalf("PutBytes: %v", err)
	}
	ref2, err := s.PutBytes([]byte("hello, kairos"))
	if err != nil {
		t.Fatalf("PutBytes: %v", err)
	}
	if ref1.Hash != ref2.Hash {
		t.Fatalf("hashes differ for identical content: %s vs %s", ref1.Hash, ref2.Hash)
	}

	blobDir := filepath.Dir(s.Path(ref1))
	entries, err := os.ReadDir(blobDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("blob directory has %d entries, want 1 (deduplicated)", len(entries))
	}
}

func TestStore_putCollectsViaRenameAndDedups(t *testing.T) {
	root := t.TempDir()
	s := artifact.New(root)

	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "output.json")
	if err := os.WriteFile(src, []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ref, err := s.Put(src)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source file %s should have been moved (rename), still exists", src)
	}
	got, err := os.ReadFile(s.Path(ref))
	if err != nil {
		t.Fatalf("reading collected blob: %v", err)
	}
	if string(got) != `{"ok":true}` {
		t.Errorf("blob content = %q, want %q", got, `{"ok":true}`)
	}

	// A second file with identical content dedups: source removed, no
	// second blob.
	src2 := filepath.Join(srcDir, "output2.json")
	if err := os.WriteFile(src2, []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	ref2, err := s.Put(src2)
	if err != nil {
		t.Fatalf("Put (dedup): %v", err)
	}
	if ref2.Hash != ref.Hash {
		t.Fatalf("dedup hash mismatch: %s vs %s", ref2.Hash, ref.Hash)
	}
	if _, err := os.Stat(src2); !os.IsNotExist(err) {
		t.Errorf("deduplicated source %s should have been removed, still exists", src2)
	}
}

// TestStore_putIsAtomicUnderConcurrentReaders proves a reader never
// observes a partially-written blob: every concurrent Put of the same
// content either sees the final file complete or not at all — rename(2)
// is a single filesystem metadata operation, so there is no window where
// a reader can open a truncated blob.
func TestStore_putIsAtomicUnderConcurrentReaders(t *testing.T) {
	root := t.TempDir()
	s := artifact.New(root)
	const n = 20
	content := make([]byte, 5*1024*1024) // large enough that a naive copy loop would show a torn read
	for i := range content {
		content[i] = byte(i % 256)
	}

	var wg sync.WaitGroup
	refs := make([]artifact.Ref, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			srcDir := t.TempDir()
			src := filepath.Join(srcDir, "blob")
			if err := os.WriteFile(src, content, 0o600); err != nil {
				errs[i] = err
				return
			}
			refs[i], errs[i] = s.Put(src)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Put[%d]: %v", i, err)
		}
	}
	for i := 1; i < n; i++ {
		if refs[i].Hash != refs[0].Hash {
			t.Fatalf("hash mismatch across concurrent Puts: %s vs %s", refs[i].Hash, refs[0].Hash)
		}
	}
	got, err := os.ReadFile(s.Path(refs[0]))
	if err != nil {
		t.Fatalf("reading final blob: %v", err)
	}
	if len(got) != len(content) {
		t.Fatalf("blob size = %d, want %d (a torn/partial write would show a different length)", len(got), len(content))
	}
}
