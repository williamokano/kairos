// Package artifact is the content-addressed blob store 06-durability.md
// specifies for anything too large to inline in an event payload:
// ~/.kairos/artifacts/blobs/sha256/<first-2-hex>/<full-hex>. Deduplicated
// by construction — the same content always hashes to the same path, so a
// second write of identical bytes is a no-op collision, never a second
// copy on disk.
package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/oklog/ulid/v2"
)

// Ref identifies one stored blob — the same shape as domain.ArtifactRef,
// kept as an independent type here since this package sits below
// internal/domain in the dependency graph (internal/domain imports
// nothing from internal/, AGENTS §2) and must not import it back.
type Ref struct {
	Hash string
	Size int64
}

// Store is a content-addressed blob store rooted at one directory.
type Store struct {
	root string
}

// New returns a Store rooted at root (typically $KAIROS_HOME/artifacts).
// The directory is created lazily by the first Put/PutBytes call, not
// here — matching internal/workspace.New's constructor style (no I/O in a
// constructor, AGENTS §3).
func New(root string) *Store {
	return &Store{root: root}
}

// blobPath returns where hash's content lives: blobs/sha256/<aa>/<hash>.
func (s *Store) blobPath(hash string) string {
	return filepath.Join(s.root, "blobs", "sha256", hash[:2], hash)
}

// Path returns the on-disk path of the blob named by ref, for a caller
// that already holds a Ref and wants to read the content back.
func (s *Store) Path(ref Ref) string {
	return s.blobPath(ref.Hash)
}

// PutBytes stores data, deduplicated by its SHA-256 hash, and returns its
// Ref. Used for payloads materialised only in memory (an oversized
// NodeOutputReceived body) — see Put for collecting an existing file.
func (s *Store) PutBytes(data []byte) (Ref, error) {
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	dest := s.blobPath(hash)

	if _, err := os.Stat(dest); err == nil {
		return Ref{Hash: hash, Size: int64(len(data))}, nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return Ref{}, fmt.Errorf("creating blob directory: %w", err)
	}
	tmp := dest + ".tmp." + ulid.Make().String()
	if err := os.WriteFile(tmp, data, 0o400); err != nil {
		return Ref{}, fmt.Errorf("writing blob: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return Ref{}, fmt.Errorf("renaming blob into place: %w", err)
	}
	return Ref{Hash: hash, Size: int64(len(data))}, nil
}

// Put collects srcPath into the store via rename(2) — "node-end collection
// becomes a rename(2) into the content-addressed store" (06-durability.md)
// — not a copy. srcPath must be on the same filesystem as the store root
// (both live under $KAIROS_HOME by construction) for the rename to be a
// single atomic metadata operation rather than falling back to a copy.
//
// Computing the hash requires one full read of srcPath first (an
// unavoidable pass — you cannot name a blob by its content before reading
// the content), but the final placement is the rename, not a second
// read-and-write copy loop; a concurrent reader of the store never
// observes a partially-written blob, since the file only ever appears at
// its final path once fully written elsewhere and renamed atomically.
func (s *Store) Put(srcPath string) (Ref, error) {
	f, err := os.Open(srcPath)
	if err != nil {
		return Ref{}, fmt.Errorf("opening %s: %w", srcPath, err)
	}
	h := sha256.New()
	size, err := io.Copy(h, f)
	closeErr := f.Close()
	if err != nil {
		return Ref{}, fmt.Errorf("hashing %s: %w", srcPath, err)
	}
	if closeErr != nil {
		return Ref{}, fmt.Errorf("closing %s: %w", srcPath, closeErr)
	}

	hash := hex.EncodeToString(h.Sum(nil))
	dest := s.blobPath(hash)

	if _, err := os.Stat(dest); err == nil {
		// Dedup hit: the content already lives in the store under this
		// hash. srcPath is redundant — remove it rather than leaving two
		// copies of the same bytes on disk.
		if err := os.Remove(srcPath); err != nil {
			return Ref{}, fmt.Errorf("removing deduplicated source %s: %w", srcPath, err)
		}
		return Ref{Hash: hash, Size: size}, nil
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return Ref{}, fmt.Errorf("creating blob directory: %w", err)
	}
	if err := os.Rename(srcPath, dest); err != nil {
		return Ref{}, fmt.Errorf("renaming %s into the store: %w", srcPath, err)
	}
	if err := os.Chmod(dest, 0o400); err != nil {
		return Ref{}, fmt.Errorf("making blob read-only: %w", err)
	}
	return Ref{Hash: hash, Size: size}, nil
}
