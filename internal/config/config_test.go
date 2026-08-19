package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/williamokano/kairos/internal/config"
)

func TestLoad_defaultsToDotKairosUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KAIROS_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := filepath.Join(home, ".kairos")
	if cfg.Home != want {
		t.Errorf("Home = %q, want %q", cfg.Home, want)
	}
	assertDirMode0700(t, cfg.Home)
}

func TestLoad_honoursKairosHomeOverride(t *testing.T) {
	want := filepath.Join(t.TempDir(), "custom-kairos")
	t.Setenv("KAIROS_HOME", want)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Home != want {
		t.Errorf("Home = %q, want %q", cfg.Home, want)
	}
	assertDirMode0700(t, cfg.Home)
}

func TestLoad_honoursXDGStateHomeOverride(t *testing.T) {
	t.Setenv("KAIROS_HOME", "")
	xdg := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdg)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(xdg, "kairos")
	if cfg.Home != want {
		t.Errorf("Home = %q, want %q", cfg.Home, want)
	}
	assertDirMode0700(t, cfg.Home)
}

func TestLoad_idempotentReLoad(t *testing.T) {
	want := filepath.Join(t.TempDir(), "kairos")
	t.Setenv("KAIROS_HOME", want)

	if _, err := config.Load(); err != nil {
		t.Fatalf("first Load: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if cfg.Home != want {
		t.Errorf("Home = %q, want %q", cfg.Home, want)
	}
}

func assertDirMode0700(t *testing.T, dir string) {
	t.Helper()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", dir)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("mode = %v, want %v", got, os.FileMode(0o700))
	}
}
