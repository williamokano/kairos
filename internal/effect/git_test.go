package effect_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/williamokano/kairos/internal/effect"
	"github.com/williamokano/kairos/internal/executor/local"
)

// runCmd runs a real git command directly (test setup only — the code
// under test is what must route through internal/executor/local, not
// this fixture-building helper).
func runCmd(t *testing.T, dir string, argv ...string) {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v\n%s", argv, err, out)
	}
}

// newLocalRemoteAndClone builds a bare "remote" repo and a clone pointing
// at it via a plain filesystem path — real git, no network, matching
// AGENTS §5's "integration test that reads the ambient PATH is a flaky
// test" posture by using only locally-built fixtures.
func newLocalRemoteAndClone(t *testing.T) (remoteDir, cloneDir string) {
	t.Helper()
	root := t.TempDir()
	remoteDir = filepath.Join(root, "remote.git")
	cloneDir = filepath.Join(root, "clone")

	runCmd(t, root, "git", "init", "--bare", remoteDir)
	runCmd(t, root, "git", "clone", remoteDir, cloneDir)
	if err := os.WriteFile(filepath.Join(cloneDir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCmd(t, cloneDir, "git", "add", ".")
	runCmd(t, cloneDir, "git", "commit", "-m", "init")
	runCmd(t, cloneDir, "git", "push", "origin", "HEAD:refs/heads/main")
	runCmd(t, cloneDir, "git", "checkout", "-b", "kairos/fix")
	if err := os.WriteFile(filepath.Join(cloneDir, "fix.txt"), []byte("fix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCmd(t, cloneDir, "git", "add", ".")
	runCmd(t, cloneDir, "git", "commit", "-m", "fix")
	return remoteDir, cloneDir
}

func TestGitPush_attemptPushesAndProbeConfirms(t *testing.T) {
	_, cloneDir := newLocalRemoteAndClone(t)
	exec := local.New(local.DefaultBootIDProvider())
	provider := effect.GitPush{Exec: exec}

	req := effect.Request{
		RunID: "run_1", NodeID: "n1", ExecID: "n1#a1.i1",
		Effect: "git.push", IdempotencyKey: effect.IdempotencyKey("run_1", "n1", "git.push"),
		WorkDir: cloneDir, Dir: t.TempDir(),
		Args: map[string]string{"branch": "kairos/fix"},
	}

	res, err := provider.Attempt(context.Background(), req)
	if err != nil {
		t.Fatalf("Attempt: %v", err)
	}
	if res.Outcome != effect.Applied {
		t.Fatalf("Attempt outcome = %v, want Applied (reason: %s)", res.Outcome, res.Reason)
	}
	if res.ExternalRef != "kairos/fix" {
		t.Errorf("ExternalRef = %q, want %q", res.ExternalRef, "kairos/fix")
	}

	probed, ok, err := provider.Probe(context.Background(), req)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !ok {
		t.Fatal("Probe: ok = false, want true — the branch was genuinely pushed")
	}
	if probed.Outcome != effect.Applied {
		t.Errorf("Probe outcome = %v, want Applied", probed.Outcome)
	}
}

func TestGitPush_probeIsUnknownWhenBranchWasNeverPushed(t *testing.T) {
	_, cloneDir := newLocalRemoteAndClone(t)
	exec := local.New(local.DefaultBootIDProvider())
	provider := effect.GitPush{Exec: exec}

	req := effect.Request{
		RunID: "run_1", NodeID: "n1", ExecID: "n1#a1.i1",
		Effect: "git.push", WorkDir: cloneDir, Dir: t.TempDir(),
		Args: map[string]string{"branch": "kairos/fix"},
	}
	_, ok, err := provider.Probe(context.Background(), req)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if ok {
		t.Fatal("Probe: ok = true for a branch that was never pushed — want false (effect.unknown)")
	}
}

func TestGitPush_isNotCompensable(t *testing.T) {
	exec := local.New(local.DefaultBootIDProvider())
	provider := effect.GitPush{Exec: exec}
	err := provider.Compensate(context.Background(), effect.Request{}, "kairos/fix")
	if !strings.Contains(err.Error(), "not compensable") {
		t.Fatalf("Compensate error = %v, want ErrNotCompensable", err)
	}
}
