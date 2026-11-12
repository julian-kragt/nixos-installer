package stages

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/julian-kragt/nixos-installer/internal/machine"
	"github.com/julian-kragt/nixos-installer/internal/pipeline"
)

const secretsCommit = "5c6778e007674df78a3bef30b28f805e5c1de32e"

// lockFile is a flake.lock that locks nix-secrets at rev.
func lockFile(rev string) string {
	return `{"root":"root","nodes":{"root":{"inputs":{"nix-secrets":"nix-secrets"}},"nix-secrets":{"locked":{"rev":"` + rev + `"}}},"version":7}`
}

// flakeEnv returns an Env for myhost whose config repo is pushed to a bare
// remote and locks nix-secrets at an old rev. A fake nix's `flake update`
// locks it at $FAKE_REV, which starts as the recorded SecretsCommit.
func flakeEnv(t *testing.T) (*pipeline.Env, string) {
	t.Helper()
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t"} {
		t.Setenv(k, v)
	}
	bin := t.TempDir()
	put(t, bin, "nix", `#!/bin/sh
[ "$1 $2 $3 $4" = "flake update nix-secrets --flake" ] || exit 1
printf '{"root":"root","nodes":{"root":{"inputs":{"nix-secrets":"nix-secrets"}},"nix-secrets":{"locked":{"rev":"%s"}}},"version":7}' "$FAKE_REV" > "$5/flake.lock"
`)
	if err := os.Chmod(filepath.Join(bin, "nix"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_REV", secretsCommit)

	remote := t.TempDir()
	gitIn(t, remote, "init", "-q", "--bare", "-b", "main")
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	put(t, dir, "flake.lock", lockFile("9f435ef0bb7d6dd3aed66f8e48169323e58ca2a5"))
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "init")
	gitIn(t, dir, "remote", "add", "origin", remote)
	gitIn(t, dir, "push", "-q", "-u", "origin", "main")

	env := &pipeline.Env{
		Inputs: pipeline.Inputs{Host: "myhost", ConfigDir: dir, SecretsDir: t.TempDir()},
		State:  pipeline.NewState(t.TempDir(), "myhost"),
		Local:  machine.Local(),
	}
	env.State.SecretsCommit = secretsCommit
	return env, remote
}

func readLock(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "flake.lock"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestFlakeInputLocksCommitsAndPushesOnce(t *testing.T) {
	env, remote := flakeEnv(t)
	ctx := context.Background()
	dir := env.ConfigDir
	start := gitIn(t, dir, "rev-parse", "HEAD")

	for range 2 {
		if err := flakeInput(ctx, env); err != nil {
			t.Fatalf("3.1: %v", err)
		}
	}
	if n := gitIn(t, dir, "rev-list", "--count", start+"..HEAD"); n != "1" {
		t.Errorf("%s commits, want 1", n)
	}
	if head := gitIn(t, dir, "rev-parse", "HEAD"); env.State.ConfigRev != head || gitIn(t, remote, "rev-parse", "main") != head {
		t.Errorf("ConfigRev = %q, HEAD %q, remote %q", env.State.ConfigRev, head, gitIn(t, remote, "rev-parse", "main"))
	}
	if msg := gitIn(t, dir, "log", "-1", "--format=%s"); msg != "chore(myhost): lock nix-secrets at 5c6778e" {
		t.Errorf("message = %q", msg)
	}
	if reason, err := flakeInputOutdated(ctx, env); err != nil || reason != "" {
		t.Errorf("outdated = %q, %v", reason, err)
	}
}

// A remote that moved since 2.4 fails 3.1 and reopens 2.4 and 2.5, which
// record and check the commit the clone is at after a pull.
func TestFlakeInputRefusesAnotherRev(t *testing.T) {
	env, _ := flakeEnv(t)
	env.State.Completed = []string{"2.3", "2.4", "2.5", "3.2", "3.3"}
	t.Setenv("FAKE_REV", "1111111111111111111111111111111111111111")
	dir := env.ConfigDir
	start, lock := gitIn(t, dir, "rev-parse", "HEAD"), readLock(t, dir)

	err := flakeInput(context.Background(), env)
	if err == nil || !strings.Contains(err.Error(), "branch moved") || !strings.Contains(err.Error(), "repeats stage 2.4 and every stage after it") {
		t.Fatalf("err = %v, want it to explain the moved branch and the way forward", err)
	}
	if got := strings.Join(env.State.Completed, ","); got != "2.3" {
		t.Errorf("completed = %s, want 2.3", got)
	}
	if head := gitIn(t, dir, "rev-parse", "HEAD"); head != start || readLock(t, dir) != lock {
		t.Error("the config was changed")
	}
	if env.State.ConfigRev != "" {
		t.Errorf("ConfigRev = %q", env.State.ConfigRev)
	}
}

func TestFlakeInputPutsTheConfigBackWhenThePushFails(t *testing.T) {
	env, remote := flakeEnv(t)
	dir := env.ConfigDir
	start, lock := gitIn(t, dir, "rev-parse", "HEAD"), readLock(t, dir)
	// Refuse every push that gets past the dry run.
	put(t, remote, "hooks/pre-receive", "#!/bin/sh\nexit 1\n")
	if err := os.Chmod(filepath.Join(remote, "hooks/pre-receive"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := flakeInput(context.Background(), env)
	if err == nil || !strings.Contains(err.Error(), "push "+dir) {
		t.Fatalf("err = %v, want a failed push", err)
	}
	if head := gitIn(t, dir, "rev-parse", "HEAD"); head != start || readLock(t, dir) != lock {
		t.Error("the config was not put back")
	}
	if status := gitIn(t, dir, "status", "--porcelain"); status != "" {
		t.Errorf("left uncommitted: %q", status)
	}
}

func TestFlakeInputRefusesAnUnpushableRepo(t *testing.T) {
	env, _ := flakeEnv(t)
	gitIn(t, env.ConfigDir, "remote", "set-url", "--push", "origin", filepath.Join(t.TempDir(), "missing"))
	start := gitIn(t, env.ConfigDir, "rev-parse", "HEAD")

	err := flakeInput(context.Background(), env)
	if err == nil || !strings.Contains(err.Error(), "cannot push") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(readLock(t, env.ConfigDir), secretsCommit) || gitIn(t, env.ConfigDir, "rev-parse", "HEAD") != start {
		t.Error("the config was changed")
	}
}

func TestFlakeInputRefusesAnUncommittedLockFile(t *testing.T) {
	env, _ := flakeEnv(t)
	put(t, env.ConfigDir, "flake.lock", lockFile("2222222222222222222222222222222222222222"))
	err := flakeInput(context.Background(), env)
	if err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("err = %v", err)
	}
}

func TestFlakeInputOutdated(t *testing.T) {
	env, _ := flakeEnv(t)
	put(t, env.ConfigDir, "flake.lock", lockFile(secretsCommit))
	tests := []struct {
		name, configRev, secretsCommit, wantReason string
	}{
		{"locked", "abc", secretsCommit, ""},
		{"no config revision", "", secretsCommit, "no config revision is recorded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env.State.ConfigRev, env.State.SecretsCommit = tt.configRev, tt.secretsCommit
			reason, err := flakeInputOutdated(context.Background(), env)
			if err != nil || reason != tt.wantReason {
				t.Errorf("reason = %q, %v, want %q", reason, err, tt.wantReason)
			}
		})
	}
}

// After another host's install moved the config's lock, 2.4 runs again so a
// new SecretsCommit is recorded for 3.1 to lock; before 3.1 it is not asked.
func TestCommitPushOutdatedWhenTheLockMoved(t *testing.T) {
	env, _ := flakeEnv(t)
	put(t, env.ConfigDir, "flake.lock", lockFile("4444444444444444444444444444444444444444"))
	tests := []struct {
		name       string
		completed  []string
		recorded   string
		wantReason string
	}{
		{"before 3.1", []string{"2.4"}, secretsCommit, ""},
		{"lock moved", []string{"2.4", "2.5", "3.1"}, secretsCommit, "the config locks nix-secrets at 4444444, not at 5c6778e"},
		{"lock in place", []string{"2.4", "2.5", "3.1"}, "4444444444444444444444444444444444444444", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env.State.Completed, env.State.SecretsCommit = tt.completed, tt.recorded
			reason, err := commitPushOutdated(context.Background(), env)
			if err != nil || reason != tt.wantReason {
				t.Errorf("reason = %q, %v, want %q", reason, err, tt.wantReason)
			}
		})
	}
}

func TestRebuildNeedsAConfigRevision(t *testing.T) {
	env, _ := flakeEnv(t) // records no ConfigRev
	if err := rebuild(context.Background(), env); err == nil || !strings.Contains(err.Error(), "run stage 3.1 first") {
		t.Fatalf("err = %v, want a missing config revision", err)
	}
}
