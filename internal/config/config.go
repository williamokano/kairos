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
	// LLMBinary is the CLI binary claude/codex/gemini/local-kind actor
	// nodes invoke (engine.Config.LLMBinary, L08). Empty means no
	// llm-kind node can run.
	LLMBinary string
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

	return Config{
		Home:                    home,
		WorkspaceRepo:           v.GetString("WORKSPACE_REPO"),
		LLMBinary:               v.GetString("LLM_BINARY"),
		AdmissionNodeSlots:      nodeSlots,
		AdmissionMaxQueued:      maxQueued,
		DailyUSD:                dailyUSD,
		ConstitutionProjectPath: constitutionProjectPath,
		PolicyPath:              policyPath,
		BaseRef:                 v.GetString("BASE_REF"),
		UnattendedAck:           v.GetString("UNATTENDED_ACK"),
		DryRun:                  v.GetBool("DRY_RUN"),
		MaxUnattendedPRs:        v.GetInt("MAX_UNATTENDED_PRS"),
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
