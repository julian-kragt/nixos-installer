package stages

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"installer/internal/machine"
	"installer/internal/pipeline"
)

// encrypted is a stand-in for a sops file; the stages only look at its shape.
const encrypted = "hashedPassword: ENC[AES256_GCM,data:abc,type:str]\nsops:\n  age:\n    - recipient: age1atlas\n"

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func put(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// secretsEnv returns an Env for atlas with user jkragt (from a fake nix) and a
// nix-secrets clone that has a pushed file of a user who left.
func secretsEnv(t *testing.T) (*pipeline.Env, string) {
	t.Helper()
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t"} {
		t.Setenv(k, v)
	}
	bin := t.TempDir()
	put(t, bin, "nix", `#!/bin/sh
printf '%s' '{"users":{"jkragt":{"uid":1000,"home":"/home/jkragt"}}}'
`)
	if err := os.Chmod(filepath.Join(bin, "nix"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	remote := t.TempDir()
	gitIn(t, remote, "init", "-q", "--bare", "-b", "main")
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	put(t, dir, ".sops.yaml", "keys:\n  - &host_other age1other\ncreation_rules:\n  - path_regex: ^hosts/other\\.yaml$\n    key_groups:\n      - age:\n          - *host_other\n")
	put(t, dir, "host-users/atlas-gone.yaml", encrypted)
	put(t, dir, "host-users/other-jkragt.yaml", encrypted)
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "init")
	gitIn(t, dir, "remote", "add", "origin", remote)
	gitIn(t, dir, "push", "-q", "-u", "origin", "main")

	env := &pipeline.Env{
		Inputs: pipeline.Inputs{Host: "atlas", ConfigDir: t.TempDir(), SecretsDir: dir},
		State:  &pipeline.State{Host: "atlas", HostRecipient: "age1atlas", UserRecipients: map[string]string{"jkragt": "age1jkragt"}},
		Local:  machine.Local(),
	}
	return env, remote
}

func TestRecipientsThenCommitPushMakeOneCommit(t *testing.T) {
	env, remote := secretsEnv(t)
	ctx := context.Background()
	dir := env.SecretsDir

	if err := secretsRepository(ctx, env); err != nil {
		t.Fatalf("2.1: %v", err)
	}
	if err := recipients(ctx, env); err != nil {
		t.Fatalf("2.2: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "host-users/atlas-gone.yaml")); !os.IsNotExist(err) {
		t.Errorf("file of the removed user: %v, want it deleted", err)
	}
	// What 2.3 would have encrypted.
	put(t, dir, "hosts/atlas.yaml", encrypted)
	put(t, dir, "host-users/atlas-jkragt.yaml", encrypted)

	start := gitIn(t, dir, "rev-parse", "HEAD")
	for range 2 {
		if err := commitPush(ctx, env); err != nil {
			t.Fatalf("2.4: %v", err)
		}
	}
	if n := gitIn(t, dir, "rev-list", "--count", start+"..HEAD"); n != "1" {
		t.Errorf("%s commits, want 1", n)
	}
	if head := gitIn(t, dir, "rev-parse", "HEAD"); env.State.SecretsCommit != head || gitIn(t, remote, "rev-parse", "main") != head {
		t.Errorf("SecretsCommit = %q, HEAD %q, remote %q", env.State.SecretsCommit, head, gitIn(t, remote, "rev-parse", "main"))
	}
	if status := gitIn(t, dir, "status", "--porcelain"); status != "" {
		t.Errorf("left uncommitted: %q", status)
	}
	if files := gitIn(t, dir, "ls-files"); strings.Contains(files, "atlas-gone") || !strings.Contains(files, "other-jkragt") {
		t.Errorf("tracked files:\n%s", files)
	}
	if err := secretsRepository(ctx, env); err != nil {
		t.Errorf("2.1 after the push: %v", err)
	}
}

func TestCommitPushRefusesPlaintext(t *testing.T) {
	env, _ := secretsEnv(t)
	ctx := context.Background()
	if err := recipients(ctx, env); err != nil {
		t.Fatal(err)
	}
	put(t, env.SecretsDir, "hosts/atlas.yaml", "placeholder: unused\n")
	put(t, env.SecretsDir, "host-users/atlas-jkragt.yaml", encrypted)
	start := gitIn(t, env.SecretsDir, "rev-parse", "HEAD")

	err := commitPush(ctx, env)
	if err == nil || !strings.Contains(err.Error(), "hosts/atlas.yaml") {
		t.Fatalf("err = %v, want the plaintext file named", err)
	}
	if head := gitIn(t, env.SecretsDir, "rev-parse", "HEAD"); head != start {
		t.Error("committed despite the plaintext")
	}
}

func TestSecretsRepositoryRefusesUnrelatedChanges(t *testing.T) {
	env, _ := secretsEnv(t)
	put(t, env.SecretsDir, "host-users/other-jkragt.yaml", "changed")
	if err := secretsRepository(context.Background(), env); err == nil || !strings.Contains(err.Error(), "other-jkragt") {
		t.Errorf("err = %v, want the unrelated change named", err)
	}
}
