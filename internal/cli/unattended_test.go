package cli_test

import (
	"testing"

	"github.com/williamokano/kairos/internal/cli"
)

// TestRunUnattended_refusesWithoutTheAcknowledgementString is
// 05-gates.md's "kairos run --unattended refuses unless config contains
// unattended.iUnderstandEffectsWillNotBeConfirmed" made real: no daemon
// is even contacted (the refusal happens before ensureClient) so this
// test needs no running daemon.
func TestRunUnattended_refusesWithoutTheAcknowledgementString(t *testing.T) {
	t.Run("missing ack", func(t *testing.T) {
		t.Setenv("KAIROS_HOME", t.TempDir())
		root := cli.RootCommand()
		root.SetArgs([]string{"run", "--unattended", "somefile.yaml"})
		err := root.Execute()
		if err != cli.ErrUnattendedAckMissing {
			t.Fatalf("err = %v, want ErrUnattendedAckMissing", err)
		}
	})

	t.Run("wrong-shaped ack", func(t *testing.T) {
		t.Setenv("KAIROS_HOME", t.TempDir())
		t.Setenv("KAIROS_UNATTENDED_ACK", "sure-why-not")
		root := cli.RootCommand()
		root.SetArgs([]string{"run", "--unattended", "somefile.yaml"})
		err := root.Execute()
		if err != cli.ErrUnattendedAckMissing {
			t.Fatalf("err = %v, want ErrUnattendedAckMissing", err)
		}
	})
}
