//go:build violation

// Package noexecoutsideexecutor is a deliberate architecture-test violation
// fixture: it imports os/exec outside internal/executor/local, which
// TestArchitecture_noExecOutsideExecutor must catch.
package noexecoutsideexecutor

import "os/exec"

var _ = exec.Command
