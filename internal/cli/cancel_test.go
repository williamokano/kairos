package cli_test

import (
	"testing"

	"github.com/williamokano/kairos/internal/cli"
)

// TestCancelCmd_reasonRequiredNoRubberStampFlags mirrors
// TestApproveCmd_hasNoRubberStampFlags's discipline for the new `kairos
// cancel` verb: --reason must exist and be required (checked below via
// the command's own RunE, since cobra's Required marking isn't otherwise
// introspectable), and no --yes/--all/-f bypass flag may ever exist.
func TestCancelCmd_reasonRequiredNoRubberStampFlags(t *testing.T) {
	root := cli.RootCommand()
	cancel, _, err := root.Find([]string{"cancel"})
	if err != nil {
		t.Fatalf("Find(cancel): %v", err)
	}

	if fl := cancel.Flags().Lookup("reason"); fl == nil {
		t.Error("kairos cancel must have a --reason flag")
	}
	for _, forbidden := range []string{"yes", "all", "compensate"} {
		if fl := cancel.Flags().Lookup(forbidden); fl != nil {
			t.Errorf("kairos cancel must never have a --%s flag (rubber-stamp bypass, or a toggle for behavior that is actually unconditional — see internal/engine/cancel.go)", forbidden)
		}
	}
	if fl := cancel.Flags().ShorthandLookup("f"); fl != nil {
		t.Error("kairos cancel must never have a -f shorthand flag (rubber-stamp bypass)")
	}
}

// TestCancelCmd_missingReasonIsRejectedBeforeTouchingTheDaemon proves the
// --reason requirement is enforced by cancel.go's own usage check (a
// usageError, exit code 2) rather than merely documented — and that it
// fires before ensureClient ever tries to reach a daemon (no DaemonStarter
// wired here at all; a reachability attempt would panic/fail differently).
func TestCancelCmd_missingReasonIsRejectedBeforeTouchingTheDaemon(t *testing.T) {
	code := cli.Execute([]string{"cancel", "run_1"}, nil, nil, nil, nil)
	if code != 2 {
		t.Errorf("exit code = %d, want 2 (usage error) for a missing --reason", code)
	}
}
