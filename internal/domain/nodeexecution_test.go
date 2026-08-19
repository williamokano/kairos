package domain_test

import (
	"testing"

	"github.com/williamokano/kairos/internal/domain"
)

func TestExecStatus_terminalStatusesAreExactlyTheClosedTerminalSet(t *testing.T) {
	terminal := map[domain.ExecStatus]bool{
		domain.ExecSucceeded:   true,
		domain.ExecFailed:      true,
		domain.ExecRejected:    true,
		domain.ExecInterrupted: true,
		domain.ExecLost:        true,
		domain.ExecParked:      true,
	}
	all := []domain.ExecStatus{
		domain.ExecPending, domain.ExecExecuting, domain.ExecWaiting, domain.ExecAdopted,
		domain.ExecSucceeded, domain.ExecFailed, domain.ExecRejected,
		domain.ExecInterrupted, domain.ExecLost, domain.ExecParked,
	}
	for _, s := range all {
		if got, want := s.Terminal(), terminal[s]; got != want {
			t.Errorf("ExecStatus(%q).Terminal() = %v, want %v", s, got, want)
		}
	}
}

func TestExecStatus_adoptedIsNotTerminal(t *testing.T) {
	// Adopted means "a surviving process was re-attached to" — still
	// in-flight, not a final outcome, even though only L06 can reach it.
	if domain.ExecAdopted.Terminal() {
		t.Error("expected ExecAdopted to not be Terminal")
	}
}

func TestExecStatus_validRejectsUnknownValues(t *testing.T) {
	if domain.ExecStatus("bogus").Valid() {
		t.Error("expected an unknown ExecStatus to be invalid")
	}
	if !domain.ExecWaiting.Valid() {
		t.Error("expected ExecWaiting to be valid")
	}
}
