package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/williamokano/kairos/internal/admission"
	"github.com/williamokano/kairos/internal/api"
	"github.com/williamokano/kairos/internal/cli"
	"github.com/williamokano/kairos/internal/config"
	"github.com/williamokano/kairos/internal/engine"
	"github.com/williamokano/kairos/internal/events"
	"github.com/williamokano/kairos/internal/eventstore"
	"github.com/williamokano/kairos/internal/executor/local"
	"github.com/williamokano/kairos/internal/policy"
	"github.com/williamokano/kairos/internal/tasksource"
	"github.com/williamokano/kairos/internal/web"
)

// serve is the daemon boot sequence, injected into internal/cli as a
// cli.ServeFunc. It lives here rather than in internal/cli because it
// must import internal/api and internal/engine — and dependencyDirection's
// "nothing imports internal/api" rule holds for every other package,
// cmd/kairos included in spirit but exempted in practice as the binary's
// own composition root, the same posture already held for
// os.Exit/os/exec/syscall.
//
// Boot order: claim the PID-file lock (decision #2 — no syscall.Flock;
// that's reserved to internal/executor/local), load config, open the
// event store (already migrates + verifies projections, L02), toolchain-
// presence checks (decision #6, run here since only this file may call
// exec.LookPath), construct the engine and run Reconcile to completion —
// the API does not start serving until engine.reconciled exists
// (09-cli-and-tui.md) — then start the live advance loop and bind/serve
// the API until SIGINT/SIGTERM.
//
// SIGINT/SIGTERM (Ctrl-C, `kairos down`) trigger a clean shutdown:
// engine.Stop records NodeExecutionInterrupted for every in-flight node
// BEFORE killing its process group (12-build-plan.md), then this
// function returns within a few seconds. SIGKILL to the daemon itself is
// not caught — children survive it (Setpgid detaches them from the
// daemon's process group), and the NEXT boot's Reconcile is what recovers
// them; that asymmetry is the whole reason both
// TestEngine_survivesKillMidRun and TestEngine_ctrlCInterruptsThenResumes
// exist as separate tests.
func serve(parentCtx context.Context) error {
	ctx, stop := signal.NotifyContext(parentCtx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

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
			eventstore.HumanTaskIndexProjection{},
		},
	})
	if err != nil {
		return fmt.Errorf("opening event store: %w", err)
	}
	defer func() { _ = store.Close() }()

	pol, err := policy.Load(cfg.PolicyPath)
	if err != nil {
		return fmt.Errorf("loading policy: %w", err)
	}

	eng := engine.New(engine.Config{
		Store:         store,
		Executor:      local.New(local.DefaultBootIDProvider()),
		BootID:        local.DefaultBootIDProvider(),
		WorkRoot:      filepath.Join(cfg.Home, "work"),
		WorkspaceRepo: cfg.WorkspaceRepo,
		LLMBinary:     cfg.LLMBinary,
		LLMConfigDir:  cfg.LLMConfigDir,
		KillGrace:     10 * time.Second,
		Admission: admission.Config{
			NodeSlots: cfg.AdmissionNodeSlots,
			MaxQueued: cfg.AdmissionMaxQueued,
			DailyUSD:  cfg.DailyUSD,
		},
		ConstitutionProjectPath:  cfg.ConstitutionProjectPath,
		Policy:                   pol,
		BaseRef:                  cfg.BaseRef,
		DryRun:                   cfg.DryRun,
		UnattendedEffectCeilings: unattendedEffectCeilings(cfg.MaxUnattendedPRs),
		Spawner: &engineSpawner{
			store: store,
			limits: tasksource.QueueLimits{
				MaxQueued: cfg.TriggerMaxQueued, MaxOpenDecisions: cfg.TriggerMaxOpenDecisions,
			},
		},
	})

	// Reconciliation must complete before the API starts serving —
	// "readiness flips only after engine.reconciled appears"
	// (09-cli-and-tui.md).
	if _, err := eng.Reconcile(ctx); err != nil {
		return fmt.Errorf("reconciling: %w", err)
	}
	if err := eng.Start(ctx); err != nil {
		return fmt.Errorf("starting engine: %w", err)
	}

	// The trigger manager (L16: inbox, pollers, cron) starts after the
	// engine's live loop, matching the same "engine is watching before
	// anything can create a run" ordering every prior document has
	// upheld — it never dispatches Cmds itself, it only calls
	// tasksource.CreateRun, which the engine's Subscribe loop then picks
	// up like any other run.
	triggers := tasksource.NewManager(tasksource.ManagerConfig{
		InboxDir:     filepath.Join(cfg.Home, "inbox"),
		InboxEnabled: cfg.InboxEnabled,
		Limits: tasksource.QueueLimits{
			MaxQueued: cfg.TriggerMaxQueued, MaxOpenDecisions: cfg.TriggerMaxOpenDecisions,
		},
	}, store)
	if err := triggers.Start(ctx); err != nil {
		return fmt.Errorf("starting trigger sources: %w", err)
	}
	defer triggers.Stop()

	deps := api.Deps{
		Store:          store,
		Engine:         eng,
		DoctorChecks:   toolchainChecks(),
		Deferred:       []string{"agent auth (L08)", "network egress (later)"},
		StartedAt:      time.Now(),
		DailyUSD:       cfg.DailyUSD,
		Home:           cfg.Home,
		DefaultDoActor: cfg.DefaultDoActor,
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

	// The agent-facing socket (L20, real for the first time — see
	// internal/api/agentsocket.go): a strictly smaller route table than
	// the admin socket above, so a workflow actor process cannot reach
	// approve/answer/publish/admin/start-a-run even if it somehow learned
	// this path. Nothing dials it yet (no document has built an
	// agent-initiated daemon callback path — see agentsocket.go's doc
	// comment); it exists now so the boundary is real infrastructure
	// rather than aspirational.
	agentSockPath := filepath.Join(cfg.Home, "agent.sock")
	agentLn, err := api.Listen(agentSockPath)
	if err != nil {
		return fmt.Errorf("binding agent socket: %w", err)
	}
	defer func() { _ = agentLn.Close() }()
	defer func() { _ = os.Remove(agentSockPath) }()
	agentSrv := &http.Server{Handler: api.NewAgentMux(deps)}
	agentErrCh := make(chan error, 1)
	go func() { agentErrCh <- agentSrv.Serve(agentLn) }()

	// The web UI (L20) is served from this same process — a browser
	// cannot dial the unix admin socket, so it gets its own loopback TCP
	// listener, but it is a client of the identical API surface, dialing
	// sockPath itself just like internal/cli.Client does (10-webui.md:
	// "both surfaces are clients of the same API over the same socket").
	webToken, err := web.GenerateToken()
	if err != nil {
		return fmt.Errorf("generating web token: %w", err)
	}
	tokenPath := filepath.Join(cfg.Home, "web-token")
	if err := os.WriteFile(tokenPath, []byte(webToken), 0o600); err != nil {
		return fmt.Errorf("writing web token: %w", err)
	}
	defer func() { _ = os.Remove(tokenPath) }()

	webLn, err := web.Listen(cfg.WebAddr, cfg.WebNonLoopbackAck)
	if err != nil {
		return fmt.Errorf("binding web UI listener: %w", err)
	}
	defer func() { _ = webLn.Close() }()

	webHost, webPort, err := net.SplitHostPort(cfg.WebAddr)
	if err != nil {
		return fmt.Errorf("parsing web addr: %w", err)
	}
	webSrv := &http.Server{Handler: web.NewMux(web.Deps{
		Client:       cli.NewClient(sockPath),
		SockPath:     sockPath,
		Token:        webToken,
		AllowedHosts: []string{cfg.WebAddr, "localhost:" + webPort, webHost + ":" + webPort},
	})}
	webErrCh := make(chan error, 1)
	go func() { webErrCh <- webSrv.Serve(webLn) }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = webSrv.Shutdown(shutdownCtx)
		_ = agentSrv.Shutdown(shutdownCtx)
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return eng.Stop(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case err := <-agentErrCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("agent socket server: %w", err)
	case err := <-webErrCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("web UI server: %w", err)
	}
}

// unattendedEffectCeilings builds engine.Config.UnattendedEffectCeilings
// — a zero maxPRs means "no cap" (config.Config.MaxUnattendedPRs's doc
// comment), which must NOT become a map entry of 0: engine.go's ceiling
// check treats any present entry as an active cap, and 0 would deny
// every gh.pr.create outright.
func unattendedEffectCeilings(maxPRs int) map[string]int {
	if maxPRs <= 0 {
		return nil
	}
	return map[string]int{"gh.pr.create": maxPRs}
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
