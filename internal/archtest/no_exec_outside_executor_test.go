package archtest

import "testing"

// execForbiddenImports is the set only the exempt packages below may
// import (L4′: execution has exactly one chokepoint). golang.org/x/sys/unix
// became a real dependency in L05 (internal/executor/local/bootid_darwin.go
// needs sysctl), so it is checked here alongside os/exec and syscall.
var execForbiddenImports = []string{"os/exec", "syscall", "golang.org/x/sys/unix"}

const executorLocalPkg = modulePath + "/internal/executor/local"
const executorExectestPkg = modulePath + "/internal/executor/exectest"

// execExemptPackages holds three entries with distinct, non-widening
// rationales, kept separate so the exemption can't silently grow:
//   - internal/executor/local: every workflow actor process is born here,
//     recorded before it exists and killable from the event log alone.
//   - internal/executor/exectest: the compliance suite for that one
//     executor — it needs syscall itself to assert process-group identity
//     and to signal a spawned test process, the same rationale as
//     internal/executor/local's own tests, factored into a reusable
//     package rather than copy-pasted per Executor implementation.
//   - cmd/kairos: starting the daemon itself (auto-start when the socket
//     doesn't respond) and the daemon's own doctor toolchain-presence
//     checks are the binary bootstrapping its own second role — no
//     run/workflow/actor is involved. See
//     L04-daemon-api-cli.md's decision #4.
var execExemptPackages = []string{executorLocalPkg, executorExectestPkg, mainPkg}

func execExempt(pkgPath string) bool {
	for _, p := range execExemptPackages {
		if pkgPath == p {
			return true
		}
	}
	return false
}

func TestArchitecture_noExecOutsideExecutor(t *testing.T) {
	t.Run("realTree", func(t *testing.T) {
		for _, pkg := range loadPkgs(t, nil, "./...") {
			if execExempt(pkg.PkgPath) {
				continue
			}
			if hit := importsAny(pkg, execForbiddenImports...); len(hit) > 0 {
				t.Errorf("%s imports forbidden packages: %v (only %v may)", pkg.PkgPath, hit, execExemptPackages)
			}
		}
	})

	t.Run("fixtureIsCaught", func(t *testing.T) {
		found := false
		for _, pkg := range loadPkgs(t, []string{"violation"}, "./internal/archtest/fixtures/noexecoutsideexecutor") {
			if hit := importsAny(pkg, execForbiddenImports...); len(hit) > 0 {
				found = true
			}
		}
		if !found {
			t.Fatal("expected the noexecoutsideexecutor fixture to be flagged, but the checker passed it")
		}
	})
}
