package constraint

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/williamokano/kairos/internal/admission"
	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/executor/local"
)

// cpuHeavyClass reuses admission's model-slot pool mechanism as a
// generic named concurrency pool for command gates (05-gates.md: "takes a
// permit? yes, cpu.heavy"). admission.Request.ModelClass indexes into a
// map[string]int keyed by name — it has never actually required the key
// to name a model, so this is a real pool with real capacity semantics,
// not a fiction; see L10-constraints-gates.md's Documented decisions for
// why a dedicated pool kind is not introduced for one caller.
const cpuHeavyClass = "cpu.heavy"

// evaluateCommand runs a real subprocess through internal/executor/local
// — the one execution chokepoint (AGENTS §2) — never os/exec directly.
// Mechanics, in order, matching 05-gates.md's "command" section exactly:
// take a cpu.heavy permit -> resolve the binary to its preflight absolute
// path -> exec with Setpgid (internal/executor/local's Start always sets
// this) -> compare exit code -> run the findings adapter -> release.
func (e *Evaluator) evaluateCommand(ctx context.Context, in Input) (Result, error) {
	start := time.Now()

	var claims admission.Claims
	if e.admit != nil {
		decision := e.admit.TryAdmit(admission.Request{ModelClass: cpuHeavyClass})
		if decision.Outcome != admission.Granted {
			// Queuing/backoff for a gate's own permit is Future work —
			// see L10-constraints-gates.md's Documented decisions: a
			// denied/queued cpu.heavy permit fails the gate with a clear
			// reason rather than the evaluator blocking or the engine
			// growing a second retry queue.
			return e.commandFailure(in, start, 0,
				fmt.Sprintf("cpu.heavy permit not granted: %s", decision.Reason), nil), nil
		}
		claims = decision.Claims
		defer e.admit.Release(claims)
	}

	if filepath.IsAbs(in.Gate.Workdir) {
		return Result{}, fmt.Errorf("constraint: gate %q: workdir must be relative (registry.Validate should have rejected this)", in.Gate.ID)
	}
	workDir := in.WorkDir
	if in.Gate.Workdir != "" {
		workDir = filepath.Join(in.WorkDir, in.Gate.Workdir)
	}

	binPath, err := local.LookPath(in.Gate.Command[0])
	if err != nil {
		return e.commandFailure(in, start, 0, fmt.Sprintf("resolving %q: %v", in.Gate.Command[0], err), nil), nil
	}
	argv := append([]string{binPath}, in.Gate.Command[1:]...)

	started, err := e.exec.Start(ctx, local.ExecSpec{
		RunID: in.RunID, NodeID: in.NodeID, ExecID: in.ExecID,
		Dir:     in.Dir,
		WorkDir: workDir,
		Env:     []string{"PATH=/usr/bin:/bin:/usr/local/bin", "HOME=" + in.Dir},
		Argv:    argv,
	})
	if err != nil {
		return e.commandFailure(in, start, 0, fmt.Sprintf("starting %q: %v", in.Gate.Command[0], err), nil), nil
	}

	res, err, timedOut := e.waitWithTimeout(ctx, started, in.Gate.Timeout)
	if err != nil {
		return e.commandFailure(in, start, 0, fmt.Sprintf("waiting for %q: %v", in.Gate.Command[0], err), nil), nil
	}
	if timedOut {
		return e.commandFailure(in, start, res.ExitCode, fmt.Sprintf("timed out after %s", in.Gate.Timeout), nil), nil
	}

	dur := msToDuration(start)
	stdout, _ := os.ReadFile(filepath.Join(in.Dir, "stdout.log"))
	evidence := capBytes(stdout, maxEvidenceBytes)

	if res.ExitCode == in.Gate.ExpectExitCode {
		return Result{Passed: true, Reason: fmt.Sprintf("exit code %d", res.ExitCode), ExitCode: res.ExitCode, DurationMs: dur}, nil
	}

	reason := fmt.Sprintf("exit code %d, want %d", res.ExitCode, in.Gate.ExpectExitCode)
	if in.Gate.Message != "" {
		reason = in.Gate.Message
	}

	findings := findingsFrom(in.Gate, stdout)
	if len(findings) == 0 {
		findings = []domain.Finding{{ID: in.Gate.ID, Message: reason + "\n" + string(evidence), Severity: severityOrDefault(in.Gate)}}
	}

	return Result{Passed: false, Reason: reason, ExitCode: res.ExitCode, DurationMs: dur, Findings: findings}, nil
}

// waitWithTimeout blocks for the process to exit, same as e.exec.Wait,
// but additionally kills it if timeout elapses first. local.Executor's
// Wait deliberately ignores ctx cancellation (it blocks on the real OS
// wait4 syscall, which has no context-aware variant) — a caller that
// wants a bounded wait has to race a timer against it and Cancel the
// process group itself, exactly the TERM->grace->KILL sequence every
// other cancellation path in this codebase already uses.
func (e *Evaluator) waitWithTimeout(ctx context.Context, started local.Started, timeout time.Duration) (res local.ExitResult, err error, timedOut bool) {
	if timeout <= 0 {
		res, err = e.exec.Wait(ctx, started.PID)
		return res, err, false
	}

	type waitOutcome struct {
		res local.ExitResult
		err error
	}
	done := make(chan waitOutcome, 1)
	go func() {
		r, werr := e.exec.Wait(context.Background(), started.PID)
		done <- waitOutcome{r, werr}
	}()

	select {
	case out := <-done:
		return out.res, out.err, false
	case <-time.After(timeout):
		// killGrace here is deliberately short: a gate that already blew
		// its own declared timeout should not also make the engine's
		// shard goroutine wait a second long grace period on top of it.
		_ = e.exec.Cancel(context.Background(), started.PGID, 2*time.Second)
		out := <-done
		return out.res, out.err, true
	}
}

func (e *Evaluator) commandFailure(in Input, start time.Time, exitCode int, reason string, findings []domain.Finding) Result {
	if findings == nil {
		findings = []domain.Finding{{ID: in.Gate.ID, Message: reason, Severity: severityOrDefault(in.Gate)}}
	}
	return Result{Passed: false, Reason: reason, ExitCode: exitCode, DurationMs: msToDuration(start), Findings: findings}
}
