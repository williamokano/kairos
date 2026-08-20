package constraint_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/admission"
	"github.com/williamokano/kairos/internal/constraint"
	"github.com/williamokano/kairos/internal/executor/local"
	"github.com/williamokano/kairos/internal/registry"
)

func newRealEvaluator(admit *admission.Manager) *constraint.Evaluator {
	return constraint.New(local.New(local.DefaultBootIDProvider()), admit)
}

func TestEvaluate_commandPassesOnMatchingExitCode(t *testing.T) {
	e := newRealEvaluator(admission.New(admission.Config{}))
	dir := t.TempDir()
	result, err := e.Evaluate(context.Background(), constraint.Input{
		Gate:    registry.GateDef{ID: "true-check", Kind: registry.GateCommand, Command: []string{"true"}, ExpectExitCode: 0},
		WorkDir: t.TempDir(),
		Dir:     dir,
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !result.Passed {
		t.Fatalf("Passed = false, want true; Reason = %q", result.Reason)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
}

func TestEvaluate_commandFailsOnMismatchedExitCode(t *testing.T) {
	e := newRealEvaluator(admission.New(admission.Config{}))
	dir := t.TempDir()
	result, err := e.Evaluate(context.Background(), constraint.Input{
		Gate:    registry.GateDef{ID: "false-check", Kind: registry.GateCommand, Command: []string{"false"}, ExpectExitCode: 0},
		WorkDir: t.TempDir(),
		Dir:     dir,
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if result.Passed {
		t.Fatal("Passed = true, want false")
	}
	if result.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", result.ExitCode)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("Findings = %v, want exactly one synthetic finding", result.Findings)
	}
}

// constraint.Evaluator never appends events itself — the
// "constraint.evaluated recorded for every outcome" claim is verified at
// the engine layer (internal/engine/gates_test.go's real end-to-end
// test), not here.
func TestEvaluate_commandPreflightBinaryNotFoundFailsLoudly(t *testing.T) {
	e := newRealEvaluator(admission.New(admission.Config{}))
	dir := t.TempDir()
	result, err := e.Evaluate(context.Background(), constraint.Input{
		Gate:    registry.GateDef{ID: "missing-binary", Kind: registry.GateCommand, Command: []string{"kairos-definitely-not-a-real-binary-xyz"}, ExpectExitCode: 0},
		WorkDir: t.TempDir(),
		Dir:     dir,
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if result.Passed {
		t.Fatal("Passed = true, want false")
	}
	if !strings.Contains(result.Reason, "resolving") {
		t.Errorf("Reason = %q, want it to name the preflight resolution failure", result.Reason)
	}
}

// TestEvaluate_commandOutputRingBufferCaps writes far more than
// maxEvidenceBytes to stdout and confirms the evaluator does not hold or
// inline the whole thing — bounded memory, not unbounded growth.
func TestEvaluate_commandOutputRingBufferCaps(t *testing.T) {
	e := newRealEvaluator(admission.New(admission.Config{}))
	dir := t.TempDir()

	script := filepath.Join(t.TempDir(), "spew.sh")
	// ~200 KiB of output, well over the 64 KiB evidence cap.
	if err := os.WriteFile(script, []byte(`#!/bin/sh
i=0
while [ $i -lt 4000 ]; do
  echo "0123456789012345678901234567890123456789012345"
  i=$((i+1))
done
exit 1
`), 0o700); err != nil {
		t.Fatalf("writing script: %v", err)
	}

	result, err := e.Evaluate(context.Background(), constraint.Input{
		Gate:    registry.GateDef{ID: "spew", Kind: registry.GateCommand, Command: []string{script}, ExpectExitCode: 0},
		WorkDir: t.TempDir(),
		Dir:     dir,
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if result.Passed {
		t.Fatal("Passed = true, want false")
	}
	if len(result.Findings) != 1 {
		t.Fatalf("Findings = %v, want exactly one synthetic finding", result.Findings)
	}
	if len(result.Findings[0].Message) > 64*1024+256 {
		t.Errorf("finding message is %d bytes, want it bounded near the 64 KiB evidence cap", len(result.Findings[0].Message))
	}
}

func TestEvaluate_commandRequestsCPUHeavyPermitBeforeSpawning(t *testing.T) {
	admit := admission.New(admission.Config{ModelSlots: map[string]int{"cpu.heavy": 1}})
	// Hold the only cpu.heavy slot before the gate ever runs.
	held := admit.TryAdmit(admission.Request{ModelClass: "cpu.heavy"})
	if held.Outcome != admission.Granted {
		t.Fatalf("priming the pool: got %+v, want Granted", held)
	}

	e := newRealEvaluator(admit)
	dir := t.TempDir()
	result, err := e.Evaluate(context.Background(), constraint.Input{
		Gate:    registry.GateDef{ID: "needs-permit", Kind: registry.GateCommand, Command: []string{"true"}, ExpectExitCode: 0},
		WorkDir: t.TempDir(),
		Dir:     dir,
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if result.Passed {
		t.Fatal("Passed = true, want false — the cpu.heavy pool was fully held, so no permit should have been granted")
	}
	if !strings.Contains(result.Reason, "cpu.heavy") {
		t.Errorf("Reason = %q, want it to name the exhausted cpu.heavy permit", result.Reason)
	}
}

func TestEvaluate_commandTimeoutIsEnforced(t *testing.T) {
	e := newRealEvaluator(admission.New(admission.Config{}))
	dir := t.TempDir()
	result, err := e.Evaluate(context.Background(), constraint.Input{
		Gate: registry.GateDef{
			ID: "slow", Kind: registry.GateCommand, Command: []string{"sleep", "5"},
			ExpectExitCode: 0, Timeout: 100 * time.Millisecond,
		},
		WorkDir: t.TempDir(),
		Dir:     dir,
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if result.Passed {
		t.Fatal("Passed = true, want false — the command should have been cut off by the timeout")
	}
}
