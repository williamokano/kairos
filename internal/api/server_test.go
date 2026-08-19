package api_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/williamokano/kairos/internal/api"
)

func TestListen_bindsAt0600(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "daemon.sock")
	ln, err := api.Listen(sock)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	info, err := os.Stat(sock)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %v, want 0600", got)
	}
}

func TestListen_removesAStaleSocketFile(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "daemon.sock")
	if err := os.WriteFile(sock, []byte("stale"), 0o600); err != nil {
		t.Fatalf("seeding stale socket file: %v", err)
	}
	ln, err := api.Listen(sock)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
}
