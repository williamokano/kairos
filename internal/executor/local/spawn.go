package local

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/oklog/ulid/v2"
)

// ProcRecord is proc.json's shape: the confirmed identity of a started
// process, written after Start succeeds. Reconciliation reads this back to
// probe whether the process it names is still the one it started.
type ProcRecord struct {
	PID       int       `json:"pid"`
	PGID      int       `json:"pgid"`
	Nonce     string    `json:"nonce"`
	BootID    string    `json:"bootId"`
	StartedAt time.Time `json:"startedAt"`
	Dir       string    `json:"dir"`
	Argv      []string  `json:"argv"`
}

// spawningRecord is spawning.json's shape: the intent to start a process,
// written BEFORE fork/exec. "process.spawning is committed before
// fork/exec... a crash in the gap leaves a discoverable fact... instead of
// a process nothing knows about" (01-architecture.md). This is the fact
// TestArchitecture_processesRecordedBeforeSpawn's AST check requires a
// recorder call for, lexically preceding cmd.Start().
type spawningRecord struct {
	Nonce       string    `json:"nonce"`
	Argv        []string  `json:"argv"`
	Dir         string    `json:"dir"`
	RequestedAt time.Time `json:"requestedAt"`
}

func writeAtomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling %s: %w", path, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("renaming %s to %s: %w", tmp, path, err)
	}
	return nil
}

// recordSpawning writes spawning.json — the recorder call that must
// lexically precede cmd.Start() in Start below.
func recordSpawning(dir, nonce string, argv []string, now time.Time) error {
	return writeAtomic(filepath.Join(dir, "spawning.json"), spawningRecord{
		Nonce: nonce, Argv: argv, Dir: dir, RequestedAt: now,
	})
}

// Start spawns spec.Argv as a child process: its own process group
// (Setpgid), stdout/stderr to files (never pipes an unreaped daemon could
// die mid-write on), identity recorded before and after fork/exec.
func (l *Local) Start(ctx context.Context, spec ExecSpec) (Started, error) {
	if len(spec.Argv) == 0 {
		return Started{}, fmt.Errorf("local: empty argv")
	}
	if err := os.MkdirAll(spec.Dir, 0o700); err != nil {
		return Started{}, fmt.Errorf("creating record dir: %w", err)
	}
	workDir := spec.WorkDir
	if workDir == "" {
		workDir = spec.Dir
	}
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return Started{}, fmt.Errorf("creating work dir: %w", err)
	}

	nonce := ulid.Make().String()

	if err := recordSpawning(spec.Dir, nonce, spec.Argv, time.Now().UTC()); err != nil {
		return Started{}, fmt.Errorf("recording spawning intent: %w", err)
	}

	stdout, err := os.OpenFile(filepath.Join(spec.Dir, "stdout.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return Started{}, fmt.Errorf("opening stdout.log: %w", err)
	}
	defer func() { _ = stdout.Close() }()
	stderr, err := os.OpenFile(filepath.Join(spec.Dir, "stderr.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return Started{}, fmt.Errorf("opening stderr.log: %w", err)
	}
	defer func() { _ = stderr.Close() }()

	// A plain exec.Command, not CommandContext: cancellation goes through
	// Signal's TERM->grace->KILL sequence, never a context-triggered
	// instant kill that would bypass it.
	cmd := exec.Command(spec.Argv[0], spec.Argv[1:]...)
	cmd.Dir = workDir
	cmd.Env = append(append([]string{}, spec.Env...), "KAIROS_NONCE="+nonce)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if spec.Stdin != nil {
		cmd.Stdin = bytes.NewReader(spec.Stdin)
	}
	// Setpgid detaches the child from the daemon's controlling terminal
	// and process group: Ctrl-C in your terminal does not reach it, and
	// the daemon decides its fate, recording the decision first
	// (01-architecture.md).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return Started{}, fmt.Errorf("starting process: %w", err)
	}

	pid := cmd.Process.Pid
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		pgid = pid
	}
	startedAt := time.Now().UTC()
	bootID, err := l.bootID.Current()
	if err != nil {
		bootID = ""
	}

	rec := ProcRecord{PID: pid, PGID: pgid, Nonce: nonce, BootID: bootID, StartedAt: startedAt, Dir: spec.Dir, Argv: spec.Argv}
	if err := writeAtomic(filepath.Join(spec.Dir, "proc.json"), rec); err != nil {
		_ = l.Signal(ctx, pgid, SignalKill)
		return Started{}, fmt.Errorf("recording proc.json: %w", err)
	}

	l.track(pid, cmd)

	return Started{PID: pid, PGID: pgid, Nonce: nonce, BootID: bootID, StartedAt: startedAt, Dir: spec.Dir}, nil
}
