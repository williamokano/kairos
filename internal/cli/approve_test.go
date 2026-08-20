package cli_test

import (
	"testing"

	"github.com/williamokano/kairos/internal/cli"
)

// TestApproveCmd_hasNoRubberStampFlags is 09-cli-and-tui.md's own
// discipline made a real, grep-proof test rather than a promise in a
// doc comment: --yes, --all, and -f must never exist on this command, at
// any point in its history, and --reason/--confirm must be present.
func TestApproveCmd_hasNoRubberStampFlags(t *testing.T) {
	root := cli.RootCommand()
	approve, _, err := root.Find([]string{"approve"})
	if err != nil {
		t.Fatalf("Find(approve): %v", err)
	}

	for _, forbidden := range []string{"yes", "all"} {
		if fl := approve.Flags().Lookup(forbidden); fl != nil {
			t.Errorf("kairos approve must never have a --%s flag (rubber-stamp bypass)", forbidden)
		}
	}
	if fl := approve.Flags().ShorthandLookup("f"); fl != nil {
		t.Errorf("kairos approve must never have a -f shorthand flag (rubber-stamp bypass)")
	}
	for _, required := range []string{"confirm", "reason", "node"} {
		if fl := approve.Flags().Lookup(required); fl == nil {
			t.Errorf("kairos approve must have a --%s flag", required)
		}
	}
}
