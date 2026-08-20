package tasksource_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/williamokano/kairos/internal/executor/local"
	"github.com/williamokano/kairos/internal/tasksource"
)

// writeFakePlugin writes a small, real stdio-NDJSON executable (a
// self-contained shell script; no jq dependency, so it runs anywhere)
// implementing describe/poll/ack exactly per 08-triggers.md's contract.
func writeFakePlugin(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-plugin")
	script := `#!/bin/sh
req=$(cat)
case "$req" in
  *'"op":"describe"'*)
    echo '{"v":1,"callID":"c1","ok":true,"output":{"name":"fake","kinds":["tasksource"],"ops":["describe","poll","ack"]}}'
    ;;
  *'"op":"poll"'*)
    echo '{"v":1,"callID":"c1","ok":true,"output":{"items":[{"id":"1","dedupeKey":"plugin-item-1","title":"t"}],"cursor":"c1"}}'
    ;;
  *'"op":"ack"'*)
    echo '{"v":1,"callID":"c1","ok":true,"output":{}}'
    ;;
  *'"op":"boom"'*)
    echo '{"v":1,"callID":"c1","ok":false,"error":{"code":"upstream","message":"synthetic failure"}}'
    ;;
  *)
    exit 7
    ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing fake plugin: %v", err)
	}
	return path
}

func TestPlugin_describeAndPollRoundTrip(t *testing.T) {
	path := writeFakePlugin(t)
	exec := local.New(local.DefaultBootIDProvider())
	p := &tasksource.Plugin{Name: "fake", Path: path, ScratchRoot: t.TempDir(), Exec: exec}

	desc, err := p.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if desc.Name != "fake" {
		t.Errorf("desc.Name = %q, want fake", desc.Name)
	}

	out, err := p.Poll(context.Background(), tasksource.PollInput{})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(out.Items) != 1 || out.Items[0].DedupeKey != "plugin-item-1" {
		t.Errorf("Poll output = %+v, want one item with dedupeKey plugin-item-1", out)
	}
}

func TestPlugin_nonZeroExitWithNoJSONNormalisesToInternalError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crashy-plugin")
	if err := os.WriteFile(path, []byte("#!/bin/sh\ncat >/dev/null\nexit 7\n"), 0o755); err != nil {
		t.Fatalf("writing crashy plugin: %v", err)
	}
	exec := local.New(local.DefaultBootIDProvider())
	p := &tasksource.Plugin{Name: "crashy", Path: path, ScratchRoot: t.TempDir(), Exec: exec}

	_, err := p.Describe(context.Background())
	if err == nil {
		t.Fatal("expected an error from a plugin that exits non-zero with no JSON")
	}
	var se *tasksource.SourceError
	if !asSourceError(err, &se) {
		t.Fatalf("error = %v (%T), want a *SourceError", err, err)
	}
	if se.Code != tasksource.ErrInternal {
		t.Errorf("Code = %q, want %q", se.Code, tasksource.ErrInternal)
	}
}

func asSourceError(err error, target **tasksource.SourceError) bool {
	se, ok := err.(*tasksource.SourceError)
	if ok {
		*target = se
	}
	return ok
}

func TestPlugin_secretsArriveAsEnvVarsNeverInRequestBody(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret-plugin")
	script := `#!/bin/sh
req=$(cat)
case "$req" in
  *"$KAIROS_SECRET_GITHUB_TOKEN"*) exit 9 ;; # would mean the secret leaked into the body
esac
if [ "$KAIROS_SECRET_GITHUB_TOKEN" = "s3cr3t" ]; then
  echo '{"v":1,"callID":"c1","ok":true,"output":{"name":"fake","kinds":[],"ops":[]}}'
else
  echo '{"v":1,"callID":"c1","ok":false,"error":{"code":"unauthorized","message":"no secret"}}'
fi
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing plugin: %v", err)
	}
	exec := local.New(local.DefaultBootIDProvider())
	st := openStore(t)
	p := &tasksource.Plugin{
		Name: "secret-fake", Path: path, ScratchRoot: t.TempDir(), Exec: exec, Store: st,
		Secrets: map[string]string{"github_token": "s3cr3t"},
	}

	if _, err := p.Describe(context.Background()); err != nil {
		t.Fatalf("Describe: %v", err)
	}

	envs, err := st.Read(context.Background(), "system")
	if err != nil {
		t.Fatalf("reading system stream: %v", err)
	}
	found := false
	for _, e := range envs {
		if e.EventType == "secret.accessed" {
			found = true
		}
	}
	if !found {
		t.Error("expected a secret.accessed event on the system stream")
	}
}
