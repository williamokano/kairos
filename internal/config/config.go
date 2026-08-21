// Package config resolves $KAIROS_HOME and creates it. The full config.yaml
// schema (admission, models, limits, exec — see 02-config.md) is added
// incrementally by the documents that own each subsystem; this package only
// establishes where Kairos keeps its state, because main.go and every later
// document need that answered the same way.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/spf13/viper"
)

// Config is the bootstrap configuration: where Kairos keeps its state.
type Config struct {
	// Home is $KAIROS_HOME, default ~/.kairos, respecting $XDG_STATE_HOME.
	Home string
	// WorkspaceRepo is the git repository workspace: write nodes clone
	// from (engine.Config.WorkspaceRepo, L06). Empty means no write-mode
	// node can run. One daemon-wide repo, not per-run selection — see
	// L06-workspaces.md's Future work.
	WorkspaceRepo string
	// LLMBinary is the CLI binary claude/codex/gemini/opencode/local-kind
	// actor nodes invoke (engine.Config.LLMBinary, L08). Empty means no
	// llm-kind node can run.
	LLMBinary string
	// DefaultDoActor is the actor kind `kairos do`/the web chat/the TUI
	// composer synthesize an ad hoc single-node workflow against
	// (registry.SynthesizeAdHoc's AdHocOptions.Actor) — any real actor
	// kind this engine dispatches, never hardcoded to one CLI. Defaults
	// to "claude": genuinely installed, authenticated, and proven working
	// in this environment (L22-harness-integration.md's real smoke test).
	DefaultDoActor string
	// LLMConfigDir is the pre-authenticated config directory claude/codex
	// actor nodes read credentials from (engine.Config.LLMConfigDir,
	// L22-harness-integration.md) — passed to the child as
	// `CLAUDE_CONFIG_DIR`/`CODEX_HOME` per 04-agents.md. Empty means an
	// authenticated CLI runs under the node's own per-run HOME, which for
	// claude/codex means unauthenticated (real per-actor-identity
	// provisioning is Future work; see NL-50).
	LLMConfigDir string
	// AdmissionNodeSlots is admission.nodes (L07) — concurrent node
	// executions across the whole daemon. Defaults to min(4, NumCPU/2)
	// per 02-config.md's defaults table.
	AdmissionNodeSlots int
	// AdmissionMaxQueued is admission.maxQueued (L07) — past this many
	// queued node executions, admission rejects rather than queues.
	// Defaults to 40 per 02-config.md.
	AdmissionMaxQueued int
	// DailyUSD is limits.dailyUSD (L07) — see admission.Request's caveat
	// that this is checked against declared/estimated cost, never metered
	// actual spend (NL-30). Defaults to 25 per 02-config.md.
	DailyUSD float64
	// ConstitutionProjectPath is 05-gates.md's authoritative,
	// outside-every-workspace gate layer (engine.Config.ConstitutionProjectPath,
	// L11). Empty means no project-level gate overrides. Defaults to
	// $KAIROS_HOME/constitution.yaml — "outside every workspace" is
	// satisfied trivially, since $KAIROS_HOME is never a workflow's
	// workspace.
	ConstitutionProjectPath string
	// PolicyPath is ~/.kairos/policy.yaml (engine.Config.Policy is loaded
	// from this path, L11). Defaults to $KAIROS_HOME/policy.yaml. A
	// missing file resolves to policy.Default() — see internal/policy's
	// Load doc comment.
	PolicyPath string
	// BaseRef is the run's base ref for git-diff/regex(added-lines) gate
	// kinds (engine.Config.BaseRef, L11). Empty means those gate kinds
	// fail loudly rather than guessing "origin/main". One daemon-wide
	// default, not per-run selection — see L11-policy-secrets.md's
	// Future work.
	BaseRef string
	// UnattendedAck is 05-gates.md's headless acknowledgement string:
	// `kairos run --unattended` refuses unless this is non-empty and
	// starts with "yes-" (the doc's own example is
	// "yes-<hostname>") — a config value, never a flag, so the operator
	// has to have typed it in at some point rather than a bare --yes
	// silently skipping every confirmation. Empty means --unattended is
	// always refused.
	UnattendedAck string
	// DryRun makes every actor: effect node record effect.simulated
	// instead of performing its builtin (engine.Config.DryRun, L12).
	// Engine-wide — see engine.Config.DryRun's doc comment for why a
	// real per-run --dry-run flag is Future work.
	DryRun bool
	// MaxUnattendedPRs is 05-gates.md's maxUnattendedPRs ceiling,
	// applied to gh.pr.create (engine.Config.UnattendedEffectCeilings,
	// L12). 0 means no cap.
	MaxUnattendedPRs int
	// TriggerMaxQueued is 08-triggers.md's "queued >= maxQueued (40) ->
	// REJECT" for TRIGGER-created runs (tasksource.QueueLimits.MaxQueued,
	// L16) — distinct from AdmissionMaxQueued's node-execution-slot
	// concern, but sharing the same default per the doc's own "40" both
	// places.
	TriggerMaxQueued int
	// TriggerMaxOpenDecisions is 08-triggers.md's "maxOpenDecisions: 5" —
	// backpressure on wait:human node executions across the whole
	// daemon.
	TriggerMaxOpenDecisions int
	// InboxEnabled turns on the `~/.kairos/inbox/*.md` watcher (L16).
	// Defaults to true — the inbox is "the best local affordance in the
	// design" per 08-triggers.md and costs nothing when empty.
	InboxEnabled bool
	// WebAddr is the web UI's listen address (10-webui.md:
	// "http://127.0.0.1:7717"). Defaults to 127.0.0.1:7717. Binding a
	// non-loopback address refuses unless WebNonLoopbackAck matches
	// web.RequiredNonLoopbackAck exactly (L20).
	WebAddr string
	// WebNonLoopbackAck is the acknowledgement string required to bind
	// WebAddr to a non-loopback host — empty by default, refusing.
	WebNonLoopbackAck string
}

// Load resolves $KAIROS_HOME (env override, then $XDG_STATE_HOME, then
// ~/.kairos) and ensures the directory exists at mode 0700.
func Load() (Config, error) {
	v := viper.New()
	v.SetEnvPrefix("KAIROS")
	v.AutomaticEnv()

	home := v.GetString("HOME")
	if home == "" {
		if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
			home = filepath.Join(xdg, "kairos")
		} else {
			uh, err := os.UserHomeDir()
			if err != nil {
				return Config{}, fmt.Errorf("resolving user home: %w", err)
			}
			home = filepath.Join(uh, ".kairos")
		}
	}

	if err := os.MkdirAll(home, 0o700); err != nil {
		return Config{}, fmt.Errorf("creating kairos home %s: %w", home, err)
	}

	nodeSlots := v.GetInt("ADMISSION_NODES")
	if nodeSlots == 0 {
		nodeSlots = defaultNodeSlots()
	}
	maxQueued := v.GetInt("ADMISSION_MAX_QUEUED")
	if maxQueued == 0 {
		maxQueued = 40
	}
	dailyUSD := v.GetFloat64("DAILY_USD")
	if dailyUSD == 0 {
		dailyUSD = 25
	}

	constitutionProjectPath := v.GetString("CONSTITUTION_PATH")
	if constitutionProjectPath == "" {
		constitutionProjectPath = filepath.Join(home, "constitution.yaml")
	}
	policyPath := v.GetString("POLICY_PATH")
	if policyPath == "" {
		policyPath = filepath.Join(home, "policy.yaml")
	}

	triggerMaxQueued := v.GetInt("TRIGGER_MAX_QUEUED")
	if triggerMaxQueued == 0 {
		triggerMaxQueued = 40
	}
	triggerMaxOpenDecisions := v.GetInt("TRIGGER_MAX_OPEN_DECISIONS")
	if triggerMaxOpenDecisions == 0 {
		triggerMaxOpenDecisions = 5
	}
	inboxEnabled := true
	if s := os.Getenv("KAIROS_INBOX_ENABLED"); s != "" {
		inboxEnabled = v.GetBool("INBOX_ENABLED")
	}

	webAddr := v.GetString("WEB_ADDR")
	if webAddr == "" {
		webAddr = "127.0.0.1:7717"
	}

	defaultDoActor := v.GetString("DEFAULT_DO_ACTOR")
	if defaultDoActor == "" {
		defaultDoActor = "claude"
	}

	return Config{
		Home:                    home,
		WorkspaceRepo:           v.GetString("WORKSPACE_REPO"),
		LLMBinary:               v.GetString("LLM_BINARY"),
		DefaultDoActor:          defaultDoActor,
		LLMConfigDir:            v.GetString("LLM_CONFIG_DIR"),
		AdmissionNodeSlots:      nodeSlots,
		AdmissionMaxQueued:      maxQueued,
		DailyUSD:                dailyUSD,
		ConstitutionProjectPath: constitutionProjectPath,
		PolicyPath:              policyPath,
		BaseRef:                 v.GetString("BASE_REF"),
		UnattendedAck:           v.GetString("UNATTENDED_ACK"),
		DryRun:                  v.GetBool("DRY_RUN"),
		MaxUnattendedPRs:        v.GetInt("MAX_UNATTENDED_PRS"),
		TriggerMaxQueued:        triggerMaxQueued,
		TriggerMaxOpenDecisions: triggerMaxOpenDecisions,
		InboxEnabled:            inboxEnabled,
		WebAddr:                 webAddr,
		WebNonLoopbackAck:       v.GetString("WEB_NON_LOOPBACK_ACK"),
	}, nil
}

// defaultNodeSlots mirrors 02-config.md's admission.nodes default:
// min(4, NumCPU/2), floored at 1 so a single-core host still admits work.
func defaultNodeSlots() int {
	n := runtime.NumCPU() / 2
	if n > 4 {
		n = 4
	}
	if n < 1 {
		n = 1
	}
	return n
}
