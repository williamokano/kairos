//go:build violation

// Package tuihasnoexecution is a deliberate architecture-test violation
// fixture: it imports os/exec, which TestArchitecture_tuiHasNoExecution must
// catch.
package tuihasnoexecution

import "os/exec"

var _ = exec.Command
