package main_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestVersionCommand builds the real binary and runs it — an integration
// test, not application code, so invoking exec.Command here does not
// violate TestArchitecture_noExecOutsideExecutor.
func TestVersionCommand(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "kairos")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building kairos: %v\n%s", err, out)
	}

	out, err := exec.Command(bin, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("kairos version: %v\n%s", err, out)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(out)), "kairos ") {
		t.Errorf("unexpected output: %q", out)
	}
}
