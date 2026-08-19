package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/williamokano/kairos/internal/api"
	"github.com/williamokano/kairos/internal/cli"
	"github.com/williamokano/kairos/internal/config"
	"github.com/williamokano/kairos/internal/events"
	"github.com/williamokano/kairos/internal/eventstore"
)

// serve is the daemon boot sequence, injected into internal/cli as a
// cli.ServeFunc. It lives here rather than in internal/cli because it
// must import internal/api — and dependencyDirection's "nothing imports
// internal/api" rule holds for every other package, cmd/kairos included
// in spirit but exempted in practice as the binary's own composition
// root, the same posture already held for os.Exit/os/exec/syscall.
//
// Boot order: claim the PID-file lock (decision #2 — no syscall.Flock;
// that's reserved to internal/executor/local, which doesn't exist until
// L06), load config, open the event store (already migrates + verifies
// projections, L02), toolchain-presence checks (decision #6, run here
// since only this file may call exec.LookPath), then bind and serve the
// API until ctx is cancelled.
func serve(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	lockPath := filepath.Join(cfg.Home, "daemon.lock")
	sockPath := filepath.Join(cfg.Home, "daemon.sock")

	if err := claimLock(ctx, lockPath, sockPath); err != nil {
		return err
	}
	defer func() { _ = os.Remove(lockPath) }()

	registry, err := events.Builtin()
	if err != nil {
		return fmt.Errorf("building event registry: %w", err)
	}
	store, err := eventstore.Open(ctx, eventstore.Config{
		Path:      filepath.Join(cfg.Home, "kairos.db"),
		BackupDir: filepath.Join(cfg.Home, "backups"),
		Registry:  registry,
		Projections: []eventstore.Projection{
			eventstore.RunStateProjection{},
			eventstore.RunIndexProjection{},
		},
	})
	if err != nil {
		return fmt.Errorf("opening event store: %w", err)
	}
	defer func() { _ = store.Close() }()

	deps := api.Deps{
		Store:        store,
		DoctorChecks: toolchainChecks(),
		Deferred:     []string{"agent auth (L08)", "network egress (later)"},
		StartedAt:    time.Now(),
	}

	ln, err := api.Listen(sockPath)
	if err != nil {
		return fmt.Errorf("binding daemon socket: %w", err)
	}
	defer func() { _ = ln.Close() }()
	defer func() { _ = os.Remove(sockPath) }()

	srv := &http.Server{Handler: api.NewMux(deps)}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// claimLock implements decision #2's PID-file-plus-socket-probe scheme:
// dial the socket first (a live daemon answers); on failure, any existing
// lock/socket files are stale (their owner died, possibly via kill -9,
// without cleanup) and are removed; then O_EXCL claims the lock
// atomically, closing the TOCTOU window the dial-then-remove steps leave
// open. See adr/0012-daemon-lock-without-flock.md.
func claimLock(ctx context.Context, lockPath, sockPath string) error {
	if probeDaemon(ctx, sockPath) {
		holder := "unknown"
		if b, err := os.ReadFile(lockPath); err == nil {
			holder = strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0])
		}
		return fmt.Errorf("a daemon is already running (pid %s)", holder)
	}
	_ = os.Remove(lockPath)
	_ = os.Remove(sockPath)

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("another kairos serve is claiming the lock right now; try again")
		}
		return fmt.Errorf("claiming daemon lock: %w", err)
	}
	defer func() { _ = f.Close() }()
	_, err = fmt.Fprintf(f, "%d\n%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
	return err
}

func probeDaemon(ctx context.Context, sockPath string) bool {
	client := cli.NewClient(sockPath)
	pingCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	return client.Ping(pingCtx)
}

// toolchainChecks is the one place exec.LookPath runs: internal/api's
// /doctor handler only ever reads the cached slice this produces at boot
// (decision #6).
func toolchainChecks() []api.DoctorCheck {
	checks := []api.DoctorCheck{}
	for _, name := range []string{"git", "gh"} {
		path, err := exec.LookPath(name)
		if err != nil {
			checks = append(checks, api.DoctorCheck{Name: name, OK: false, Detail: "not found on PATH"})
			continue
		}
		checks = append(checks, api.DoctorCheck{Name: name, OK: true, Detail: path})
	}
	return checks
}
