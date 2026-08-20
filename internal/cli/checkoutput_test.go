package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/williamokano/kairos/internal/cli"
)

// TestCheckOutput exercises `kairos check-output` exactly as an actor
// process invokes it: no daemon, no flags, driven entirely by
// $KAIROS_OUTPUT/$KAIROS_SCHEMA (04-agents.md) — it must never need a
// running daemon, since an actor calls this from deep inside its own
// sandboxed subprocess.
func TestCheckOutput(t *testing.T) {
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "schema.json")
	outputPath := filepath.Join(dir, "output.json")
	if err := os.WriteFile(schemaPath, []byte(`{"type":"object","required":["ok"],"properties":{"ok":{"type":"boolean"}}}`), 0o600); err != nil {
		t.Fatalf("writing schema: %v", err)
	}

	t.Run("valid output exits 0 with no output", func(t *testing.T) {
		if err := os.WriteFile(outputPath, []byte(`{"ok":true}`), 0o600); err != nil {
			t.Fatalf("writing output: %v", err)
		}
		t.Setenv("KAIROS_OUTPUT", outputPath)
		t.Setenv("KAIROS_SCHEMA", schemaPath)

		var stdout bytes.Buffer
		root := cli.RootCommand()
		root.SetOut(&stdout)
		root.SetArgs([]string{"check-output"})
		if err := root.Execute(); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if stdout.Len() != 0 {
			t.Errorf("stdout = %q, want empty on success", stdout.String())
		}
	})

	t.Run("invalid output exits non-zero and prints violation lines", func(t *testing.T) {
		if err := os.WriteFile(outputPath, []byte(`{"ok":"not-a-bool"}`), 0o600); err != nil {
			t.Fatalf("writing output: %v", err)
		}
		t.Setenv("KAIROS_OUTPUT", outputPath)
		t.Setenv("KAIROS_SCHEMA", schemaPath)

		var stdout bytes.Buffer
		root := cli.RootCommand()
		root.SetOut(&stdout)
		root.SetArgs([]string{"check-output"})
		if err := root.Execute(); err == nil {
			t.Fatal("expected Execute to return an error for invalid output")
		}
		if stdout.Len() == 0 {
			t.Error("expected at least one violation line on stdout")
		}
	})

	t.Run("missing env vars is a usage error", func(t *testing.T) {
		t.Setenv("KAIROS_OUTPUT", "")
		t.Setenv("KAIROS_SCHEMA", "")

		root := cli.RootCommand()
		root.SetArgs([]string{"check-output"})
		if err := root.Execute(); err == nil {
			t.Fatal("expected Execute to return an error when env vars are unset")
		}
	})
}
