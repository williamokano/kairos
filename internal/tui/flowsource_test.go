package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/williamokano/kairos/internal/cli"
)

const validFlowYAMLForTUITest = `name: greet-flow
nodes:
  - id: greet
    actor: rule
    output: { x: "string" }
`

// TestFlowCreateScreen_savesARealRunnableWorkflow is a true end-to-end
// smoke test against a real daemon (matching L15's/L28's established
// pattern) — the TUI's own createFlow reads a real local file and posts
// it; the saved flow must genuinely appear via ListFlowDefinitions
// afterward.
func TestFlowCreateScreen_savesARealRunnableWorkflow(t *testing.T) {
	bin := buildKairosForSSETest(t)
	home := t.TempDir()
	sockPath, cleanup := startRealDaemon(t, bin, home)
	defer cleanup()

	client := cli.NewClient(sockPath)

	flowFile := filepath.Join(t.TempDir(), "greet-flow.yaml")
	if err := os.WriteFile(flowFile, []byte(validFlowYAMLForTUITest), 0o600); err != nil {
		t.Fatalf("writing flow file: %v", err)
	}

	m := New(context.Background(), client, home, true)
	m.flows.creating = true
	msg := runCmd(t, m.createFlow("greet-flow", flowFile))
	fm, ok := msg.(flowCreatedMsg)
	if !ok {
		t.Fatalf("createFlow produced %T, want flowCreatedMsg", msg)
	}
	if fm.err != nil {
		t.Fatalf("createFlow: %v", fm.err)
	}
	updated, _ := m.Update(fm)
	m = updated.(Model)

	view := m.viewFlowCreate()
	if !strings.Contains(view, fm.flow.Path) {
		t.Errorf("viewFlowCreate() = %q, want it to show the saved path", view)
	}

	flows, err := client.ListFlowDefinitions(context.Background())
	if err != nil {
		t.Fatalf("ListFlowDefinitions: %v", err)
	}
	if len(flows) != 1 || flows[0].Name != "greet-flow" {
		t.Fatalf("flows = %+v, want exactly one named greet-flow", flows)
	}
}

// TestFlowCreateScreen_invalidWorkflowShowsRealError proves a bad
// workflow surfaces the real registry error in the TUI, not a swallowed
// generic failure.
func TestFlowCreateScreen_invalidWorkflowShowsRealError(t *testing.T) {
	bin := buildKairosForSSETest(t)
	home := t.TempDir()
	sockPath, cleanup := startRealDaemon(t, bin, home)
	defer cleanup()

	client := cli.NewClient(sockPath)

	badFile := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(badFile, []byte("not a valid workflow"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := New(context.Background(), client, home, true)
	m.flows.creating = true
	msg := runCmd(t, m.createFlow("bad", badFile))
	fm := msg.(flowCreatedMsg)
	if fm.err == nil {
		t.Fatal("expected an error for an invalid workflow")
	}
	updated, _ := m.Update(fm)
	m = updated.(Model)
	if m.flows.saveErr == nil {
		t.Error("expected m.flows.saveErr to be set")
	}
	if !m.flows.creating {
		t.Error("a failed save must leave the create flow open for another attempt, not silently exit it")
	}
}

// TestSourceCreateScreen_createsARealCronSource proves the discrete
// schedule fields reach the daemon and produce a real, listable cron
// source — the SAME config shape `kairos src add cron`'s friendly flags
// build (internal/tasksource.BuildCronConfig), never a divergent schema.
func TestSourceCreateScreen_createsARealCronSource(t *testing.T) {
	bin := buildKairosForSSETest(t)
	home := t.TempDir()
	sockPath, cleanup := startRealDaemon(t, bin, home)
	defer cleanup()

	client := cli.NewClient(sockPath)

	m := New(context.Background(), client, home, true)
	m.cronSource.creating = true
	msg := runCmd(t, m.createCronSource("nightly", "daily", 0, 3, 30, "/tmp/flow.yaml"))
	sm, ok := msg.(cronSourceCreatedMsg)
	if !ok {
		t.Fatalf("createCronSource produced %T, want cronSourceCreatedMsg", msg)
	}
	if sm.err != nil {
		t.Fatalf("createCronSource: %v", sm.err)
	}
	updated, _ := m.Update(sm)
	m = updated.(Model)

	view := m.viewSourceCreate()
	if !strings.Contains(view, "nightly") {
		t.Errorf("viewSourceCreate() = %q, want it to show the created source id", view)
	}

	sources, err := client.ListSources(context.Background())
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	if len(sources) != 1 || sources[0].ID != "nightly" || sources[0].Kind != "cron" {
		t.Fatalf("sources = %+v, want exactly one cron source named nightly", sources)
	}
}
