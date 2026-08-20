package effect_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/williamokano/kairos/internal/effect"
	"github.com/williamokano/kairos/internal/executor/local"
)

// writeFakeGH writes a shell script standing in for the real `gh` CLI —
// never a real network call in this suite, matching L08's fake-CLI-stub
// convention for actor_llm_test.go's writeFakeLLM.
func writeFakeGH(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gh")
	full := "#!/bin/sh\n" + script + "\n"
	if err := os.WriteFile(path, []byte(full), 0o700); err != nil {
		t.Fatalf("writing fake gh script: %v", err)
	}
	return dir
}

func TestGHPRCreate_attemptSucceeds(t *testing.T) {
	binDir := writeFakeGH(t, `
if [ "$1" = "pr" ] && [ "$2" = "create" ]; then
  echo "https://github.com/acme/backend/pull/418"
  exit 0
fi
exit 1
`)
	exec := local.New(local.DefaultBootIDProvider())
	provider := effect.GHPRCreate{Exec: exec}
	req := effect.Request{
		RunID: "run_1", NodeID: "n1", ExecID: "n1#a1.i1",
		Effect: "gh.pr.create", Dir: t.TempDir(), PathPrefix: binDir,
		Args: map[string]string{"branch": "kairos/fix", "base": "main", "title": "Fix the bug"},
	}
	res, err := provider.Attempt(context.Background(), req)
	if err != nil {
		t.Fatalf("Attempt: %v", err)
	}
	if res.Outcome != effect.Applied {
		t.Fatalf("outcome = %v, want Applied (reason: %s)", res.Outcome, res.Reason)
	}
	if res.ExternalRef != "https://github.com/acme/backend/pull/418" {
		t.Errorf("ExternalRef = %q", res.ExternalRef)
	}
}

func TestGHPRCreate_attemptFails(t *testing.T) {
	binDir := writeFakeGH(t, `echo "already exists" 1>&2; exit 1`)
	exec := local.New(local.DefaultBootIDProvider())
	provider := effect.GHPRCreate{Exec: exec}
	req := effect.Request{
		RunID: "run_1", NodeID: "n1", ExecID: "n1#a1.i1",
		Effect: "gh.pr.create", Dir: t.TempDir(), PathPrefix: binDir,
		Args: map[string]string{"branch": "kairos/fix", "base": "main", "title": "Fix the bug"},
	}
	res, err := provider.Attempt(context.Background(), req)
	if err != nil {
		t.Fatalf("Attempt: %v", err)
	}
	if res.Outcome != effect.Failed {
		t.Fatalf("outcome = %v, want Failed", res.Outcome)
	}
}

func TestGHPRCreate_probeFindsExistingPR(t *testing.T) {
	binDir := writeFakeGH(t, `
if [ "$1" = "pr" ] && [ "$2" = "list" ]; then
  echo '[{"number":418,"url":"https://github.com/acme/backend/pull/418"}]'
  exit 0
fi
exit 1
`)
	exec := local.New(local.DefaultBootIDProvider())
	provider := effect.GHPRCreate{Exec: exec}
	req := effect.Request{
		RunID: "run_1", NodeID: "n1", ExecID: "n1#a1.i1",
		Effect: "gh.pr.create", Dir: t.TempDir(), PathPrefix: binDir,
		Args: map[string]string{"branch": "kairos/fix"},
	}
	res, ok, err := provider.Probe(context.Background(), req)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !ok {
		t.Fatal("Probe: ok = false, want true")
	}
	if res.ExternalRef != "https://github.com/acme/backend/pull/418" {
		t.Errorf("ExternalRef = %q", res.ExternalRef)
	}
}

func TestGHPRCreate_probeIsUnknownWhenNoPRExists(t *testing.T) {
	binDir := writeFakeGH(t, `echo '[]'; exit 0`)
	exec := local.New(local.DefaultBootIDProvider())
	provider := effect.GHPRCreate{Exec: exec}
	req := effect.Request{
		RunID: "run_1", NodeID: "n1", ExecID: "n1#a1.i1",
		Effect: "gh.pr.create", Dir: t.TempDir(), PathPrefix: binDir,
		Args: map[string]string{"branch": "kairos/fix"},
	}
	_, ok, err := provider.Probe(context.Background(), req)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if ok {
		t.Fatal("Probe: ok = true with an empty PR list — want false (effect.unknown)")
	}
}

func TestGHPRCreate_compensateClosesThePR(t *testing.T) {
	binDir := writeFakeGH(t, `
if [ "$1" = "pr" ] && [ "$2" = "close" ]; then
  exit 0
fi
exit 1
`)
	exec := local.New(local.DefaultBootIDProvider())
	provider := effect.GHPRCreate{Exec: exec}
	req := effect.Request{
		RunID: "run_1", NodeID: "n1", ExecID: "n1#a1.i1",
		Effect: "gh.pr.create", Dir: t.TempDir(), PathPrefix: binDir,
	}
	if err := provider.Compensate(context.Background(), req, "418"); err != nil {
		t.Fatalf("Compensate: %v", err)
	}
}
