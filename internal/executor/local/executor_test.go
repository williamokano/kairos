package local_test

import (
	"testing"

	"github.com/williamokano/kairos/internal/executor/exectest"
	"github.com/williamokano/kairos/internal/executor/local"
)

// TestExecutor_childInOwnProcessGroup is the milestone's named unit test
// (12-build-plan.md), run via the shared compliance suite so a future
// Executor implementation (a remote runner, 07-runners.md) can reuse it
// without rewriting the invariant.
func TestExecutor_childInOwnProcessGroup(t *testing.T) {
	exectest.RunComplianceSuite(t, func() *local.Local {
		return local.New(local.DefaultBootIDProvider())
	})
}
