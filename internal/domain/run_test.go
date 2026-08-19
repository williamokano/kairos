package domain_test

import (
	"testing"

	"github.com/williamokano/kairos/internal/domain"
)

func TestRunStatus_terminalStatusesAreExactlyTheClosedTerminalSet(t *testing.T) {
	terminal := map[domain.RunStatus]bool{
		domain.RunSucceeded:  true,
		domain.RunFailed:     true,
		domain.RunCancelledS: true,
		domain.RunRejectedS:  true,
	}
	all := []domain.RunStatus{
		domain.RunPending, domain.RunRunning, domain.RunDegradedS,
		domain.RunSucceeded, domain.RunFailed, domain.RunCancelledS, domain.RunRejectedS,
	}
	for _, s := range all {
		if got, want := s.Terminal(), terminal[s]; got != want {
			t.Errorf("RunStatus(%q).Terminal() = %v, want %v", s, got, want)
		}
	}
}

func TestRunStatus_validRejectsUnknownValues(t *testing.T) {
	if domain.RunStatus("bogus").Valid() {
		t.Error("expected an unknown RunStatus to be invalid")
	}
	if !domain.RunRunning.Valid() {
		t.Error("expected RunRunning to be valid")
	}
}

func TestRunState_terminalDelegatesToStatus(t *testing.T) {
	s := domain.RunState{Status: domain.RunSucceeded}
	if !s.Terminal() {
		t.Error("expected RunState with Status=RunSucceeded to be Terminal")
	}
	s.Status = domain.RunRunning
	if s.Terminal() {
		t.Error("expected RunState with Status=RunRunning to not be Terminal")
	}
}
