// Package api holds net/http handlers on the daemon's unix socket. It is a
// leaf: nothing in this repository imports it (internal/cli and
// internal/api each independently consult internal/apispec's Op table
// instead of importing one another — see apispec's package doc).
//
// This is the admin-facing daemon socket the CLI/TUI/web use. It is a
// different socket from the narrow, agent-facing one actors will call
// (kairos check-output/artifact stage/ask-human) once actors exist — that
// one is L08's (see TestArchitecture_agentSocketRouteSubset).
package api

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/williamokano/kairos/internal/engine"
	"github.com/williamokano/kairos/internal/eventstore"
	"github.com/williamokano/kairos/internal/project"
)

// DoctorCheck is one named pass/fail/detail result, computed once at
// daemon boot (see Deps.DoctorChecks) — GET /doctor only reads the cached
// slice; internal/api itself never calls exec.LookPath (that would need
// os/exec, reserved to cmd/kairos's own bootstrap and
// internal/executor/local — see 04-daemon-api-cli.md's decision #6).
type DoctorCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// Deps are the daemon's dependencies, assembled by cmd/kairos's serve
// bootstrap and passed to NewMux.
type Deps struct {
	Store        eventstore.Store
	Engine       *engine.Engine // AnswerHumanTask's callable core — see human.go
	DoctorChecks []DoctorCheck
	Deferred     []string // doctor checks not yet implemented, named honestly rather than omitted
	StartedAt    time.Time
	// DailyUSD is cfg.DailyUSD (02-config.md's limits.dailyUSD), the same
	// value internal/admission.Config.DailyUSD was constructed with — the
	// cost route reads it directly rather than asking the engine, since
	// the cap is configuration, not engine state.
	DailyUSD float64
	// Home is $KAIROS_HOME — POST /do (kairos do) writes ad hoc
	// single-node definitions under Home/adhoc/ via registry.SynthesizeAdHoc.
	Home string
	// DefaultDoActor is cfg.DefaultDoActor — the actor kind POST /do
	// synthesizes its ad hoc node against.
	DefaultDoActor string
	// Projects is internal/project's Manager — Project/Session CRUD and
	// worktree provisioning, backing POST /projects, POST /sessions, and
	// POST /do's --session resolution.
	Projects *project.Manager
}

// NewMux builds the daemon's route table.
func NewMux(deps Deps) *http.ServeMux {
	mux := http.NewServeMux()
	registerRunRoutes(mux, deps)
	registerDoRoutes(mux, deps)
	registerConversationRoutes(mux, deps)
	registerHumanRoutes(mux, deps)
	registerEffectsRoutes(mux, deps)
	registerWaiverRoutes(mux, deps)
	registerHumanTaskRoutes(mux, deps)
	registerForkRoutes(mux, deps)
	registerCancelRoutes(mux, deps)
	registerCompareRoutes(mux, deps)
	registerDiffRoutes(mux, deps)
	registerEventRoutes(mux, deps)
	registerStatusRoute(mux, deps)
	registerDoctorRoute(mux, deps)
	registerDBRoutes(mux, deps)
	registerBackupRoute(mux, deps)
	registerSourceRoutes(mux, deps)
	registerLifecycleRoutes(mux, deps)
	registerCostRoute(mux, deps)
	registerProjectRoutes(mux, deps)
	registerSessionRoutes(mux, deps)
	registerFSRoutes(mux, deps)
	return mux
}

// Listen binds a unix socket at path, removing a stale socket file left by
// a previous run first, and chmods it to 0600 — net.Listen on a unix
// socket does not default to a restrictive mode.
func Listen(path string) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("removing stale socket %s: %w", path, err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listening on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("chmod %s: %w", path, err)
	}
	return ln, nil
}
