package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/williamokano/kairos/internal/config"
)

// runWeb is internal/cli's WebFunc. The daemon (kairos serve) already
// binds and serves the web UI alongside the admin socket — this verb's
// whole job is minting the one-time-token URL and opening it, per
// 10-webui.md: "kairos web ensures the daemon is running, mints a
// one-time URL, and opens your browser." ensureClient (called by
// internal/cli before this runs) already guaranteed the daemon is up.
func runWeb(_ context.Context, _ /* sockPath */, homePath string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	tokenPath := filepath.Join(homePath, "web-token")
	tokenBytes, err := os.ReadFile(tokenPath)
	if err != nil {
		return fmt.Errorf("reading web token (is the daemon running with the web UI enabled?): %w", err)
	}
	url := fmt.Sprintf("http://%s/?t=%s", cfg.WebAddr, string(tokenBytes))
	fmt.Printf("opening %s (one-time)\n", url)
	return openBrowser(url)
}

// openBrowser shells out to the OS-appropriate "open a URL" command. This
// is honestly untested by CI (no real browser/display in a headless test
// runner) — see L20-webui.md's Documented decisions. A failure here is
// non-fatal to the verb's main promise (the daemon is up, the URL is
// printed); it only degrades the "opens for you" convenience.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "could not open a browser automatically: %v\n", err)
		return nil
	}
	return nil
}
