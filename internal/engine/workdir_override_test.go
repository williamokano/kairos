package engine_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/domain"
	"github.com/williamokano/kairos/internal/engine"
	"github.com/williamokano/kairos/internal/executor/local"
)

// TestEngine_workDirOverrideBecomesTheProcessRealCWD proves
// registry.NodeDef.WorkDirOverride (internal/project's Session CWD
// threading) actually changes the LLM actor's real working directory,
// not just a config value nobody reads: the fake CLI writes its genuine
// process cwd (a shell builtin, not an env var Kairos itself set) into
// the output file, so the assertion can only pass if the real subprocess
// actually started in that directory.
func TestEngine_workDirOverrideBecomesTheProcessRealCWD(t *testing.T) {
	st := openStore(t)
	home := t.TempDir()
	workRoot := filepath.Join(home, "work")
	sessionDir := t.TempDir() // stands in for a real Session's own directory
	runID := "run_workdir_override"

	fakeCLI := writeFakeLLM(t, `
echo "{\"cwd\": \"$(pwd -P)\"}" > "$KAIROS_OUTPUT"
`)

	eng := engine.New(engine.Config{
		Store:     st,
		Executor:  local.New(local.DefaultBootIDProvider()),
		BootID:    local.DefaultBootIDProvider(),
		WorkRoot:  workRoot,
		LLMBinary: fakeCLI,
		KillGrace: 200 * time.Millisecond,
	})

	yaml := `
name: workdir-override
nodes:
  - id: n1
    actor: claude
    workDirOverride: ` + sessionDir + `
    output: { cwd: "string!" }
`

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := eng.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = eng.Stop(context.Background()) }()

	writeAndStartRun(t, st, workRoot, runID, yaml)

	status := waitForTerminal(t, ctx, st, runID, 8*time.Second)
	if status != domain.RunSucceeded {
		t.Fatalf("run status = %s, want succeeded", status)
	}

	wantDir, err := filepath.EvalSymlinks(sessionDir)
	if err != nil {
		t.Fatalf("EvalSymlinks(sessionDir): %v", err)
	}

	body, err := os.ReadFile(filepath.Join(workRoot, runID, "n1#a1.i1", "output.json"))
	if err != nil {
		t.Fatalf("reading output.json: %v", err)
	}
	var out struct{ Cwd string }
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshalling output.json: %v", err)
	}
	gotDir, err := filepath.EvalSymlinks(out.Cwd)
	if err != nil {
		t.Fatalf("EvalSymlinks(gotDir=%q): %v", out.Cwd, err)
	}
	if gotDir != wantDir {
		t.Errorf("actor's real process cwd = %q, want %q (the session's WorkDirOverride)", gotDir, wantDir)
	}
}
