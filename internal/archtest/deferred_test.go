package archtest

import "testing"

// These two architecture tests from 01-architecture.md need real code
// this document must not write yet (AGENTS.md §7: build exactly the
// document in front of you). They are named exactly as specified and
// skipped, citing the document that implements them, so `go test ./...`
// enumerates all nine architecture-test names from day one without a check
// that has nothing real to check against.
// TestArchitecture_singleWriter is implemented for real in
// single_writer_test.go — L02 gives internal/eventstore its writer
// goroutine.

func TestArchitecture_processesRecordedBeforeSpawn(t *testing.T) {
	t.Skip("requires internal/executor/local's real Start(); implemented in L06")
}

func TestArchitecture_agentSocketRouteSubset(t *testing.T) {
	t.Skip("requires the daemon's route tables; implemented in L04")
}
