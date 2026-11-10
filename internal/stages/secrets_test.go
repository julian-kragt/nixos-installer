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
	return secretsEnvIn(t, "")
}

// secretsEnvIn is secretsEnv with nix-secrets in the directory sub of its git
// repository.
func secretsEnvIn(t *testing.T, sub string) (*pipeline.Env, string) {
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
	top := t.TempDir()
	dir := filepath.Join(top, sub)
	gitIn(t, top, "init", "-q", "-b", "main")
	put(t, dir, ".sops.yaml", "keys:\n  - &host_other age1other\ncreation_rules:\n  - path_regex: ^hosts/other\\.yaml$\n    key_groups:\n      - age:\n          - *host_other\n")
	put(t, dir, "host-users/atlas-gone.yaml", encrypted)
	put(t, dir, "host-users/other-jkragt.yaml", encrypted)
	gitIn(t, top, "add", ".")
	gitIn(t, top, "commit", "-q", "-m", "init")
	gitIn(t, top, "remote", "add", "origin", remote)
	gitIn(t, top, "push", "-q", "-u", "origin", "main")

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

// A run that stops after 2.2 leaves the file of the removed user deleted; 2.1
// in the next run accepts that.
func TestSecretsRepositoryAcceptsTheDeletionsOfRecipients(t *testing.T) {
	env, _ := secretsEnv(t)
	ctx := context.Background()
	if err := recipients(ctx, env); err != nil {
		t.Fatal(err)
	}
	if err := secretsRepository(ctx, env); err != nil {
		t.Errorf("2.1 after 2.2: %v", err)
	}
}

func TestCommitPushCommitsDeletionsInASubdirectory(t *testing.T) {
	env, _ := secretsEnvIn(t, "secrets")
	ctx := context.Background()
	if err := recipients(ctx, env); err != nil {
		t.Fatal(err)
	}
	put(t, env.SecretsDir, "hosts/atlas.yaml", encrypted)
	put(t, env.SecretsDir, "host-users/atlas-jkragt.yaml", encrypted)
	if err := commitPush(ctx, env); err != nil {
		t.Fatal(err)
	}
	if status := gitIn(t, env.SecretsDir, "status", "--porcelain"); status != "" {
		t.Errorf("left uncommitted: %q", status)
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

// A rule before a file's own rule, here one without path_regex, fails 2.2
// before it changes anything.
func TestRecipientsRefusesAShadowingRule(t *testing.T) {
	env, _ := secretsEnv(t)
	path := filepath.Join(env.SecretsDir, ".sops.yaml")
	put(t, env.SecretsDir, ".sops.yaml", "keys:\n  - &host_other age1other\ncreation_rules:\n"+
		"  - key_groups:\n      - age:\n          - *host_other\n"+
		"  - path_regex: ^hosts/other\\.yaml$\n    key_groups:\n      - age:\n          - *host_other\n")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	err = recipients(context.Background(), env)
	if err == nil {
		t.Fatal("2.2 accepted a catch-all before the rules")
	}
	for _, f := range []string{"hosts/atlas.yaml", "host-users/atlas-jkragt.yaml", "host-users/other-jkragt.yaml"} {
		if !strings.Contains(err.Error(), f) {
			t.Errorf("err = %v, want %s named", err, f)
		}
	}
	if strings.Contains(err.Error(), "atlas-gone") {
		t.Errorf("err = %v names the file of the removed user", err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Errorf(".sops.yaml changed:\n%s", after)
	}
	if _, err := os.Stat(filepath.Join(env.SecretsDir, "host-users/atlas-gone.yaml")); err != nil {
		t.Errorf("file of the removed user: %v, want it kept", err)
	}
}

func TestSecretsRepositoryRefusesUnrelatedChanges(t *testing.T) {
	env, _ := secretsEnv(t)
	put(t, env.SecretsDir, "host-users/other-jkragt.yaml", "changed")
	if err := secretsRepository(context.Background(), env); err == nil || !strings.Contains(err.Error(), "other-jkragt") {
		t.Errorf("err = %v, want the unrelated change named", err)
	}
}

func TestUserIdentityOutdatedWhenTheUsersChange(t *testing.T) {
	env, _ := secretsEnv(t)
	tests := []struct {
		name       string
		recorded   map[string]string
		wantReason string
	}{
		{"same users", map[string]string{"jkragt": "age1jkragt"}, ""},
		{"user removed from the host", map[string]string{"jkragt": "age1jkragt", "gone": "age1gone"}, "the users of atlas are jkragt, the recorded ones gone, jkragt"},
		{"user added to the host", nil, "the users of atlas are jkragt, the recorded ones none"},
		{"recipient missing", map[string]string{"jkragt": ""}, "no recipient is recorded for jkragt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env.State.UserRecipients = tt.recorded
			reason, err := userIdentityOutdated(context.Background(), env)
			if err != nil || reason != tt.wantReason {
				t.Errorf("reason = %q, %v, want %q", reason, err, tt.wantReason)
			}
		})
	}
}

// encryptedTo is a stand-in for a sops file encrypted to recipients.
func encryptedTo(recipients ...string) string {
	s := "hashedPassword: ENC[AES256_GCM,data:abc,type:str]\nsops:\n  age:\n"
	for _, r := range recipients {
		s += "    - recipient: " + r + "\n"
	}
	return s
}

// pushedEnv returns an Env after 2.2 to 3.1: atlas' files committed, pushed
// and recorded as SecretsCommit.
func pushedEnv(t *testing.T) *pipeline.Env {
	t.Helper()
	env, _ := secretsEnv(t)
	ctx := context.Background()
	if err := recipients(ctx, env); err != nil {
		t.Fatal(err)
	}
	put(t, env.SecretsDir, "hosts/atlas.yaml", encryptedTo("age1atlas"))
	put(t, env.SecretsDir, "host-users/atlas-jkragt.yaml", encryptedTo("age1atlas", "age1jkragt"))
	if err := commitPush(ctx, env); err != nil {
		t.Fatal(err)
	}
	env.State.ConfigRev = "c0ffee"
	return env
}

func TestPushedSecretsChecksTheCommitNotTheFiles(t *testing.T) {
	env := pushedEnv(t)
	put(t, env.SecretsDir, "hosts/atlas.yaml", "placeholder: unused\n")
	if err := pushedSecrets(context.Background(), env); err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestPushedSecretsReportsEveryProblem(t *testing.T) {
	const catchAll = "  - key_groups:\n      - age:\n          - *host_other\n"
	tests := []struct {
		name   string
		change func(t *testing.T, dir string)
		push   bool
		want   []string
	}{
		{"plaintext", func(t *testing.T, dir string) {
			put(t, dir, "hosts/atlas.yaml", "hashedPassword: plain\n")
		}, true, []string{"hosts/atlas.yaml: hashedPassword is not encrypted"}},
		{"missing user file", func(t *testing.T, dir string) {
			gitIn(t, dir, "rm", "-q", "host-users/atlas-jkragt.yaml")
		}, true, []string{"host-users/atlas-jkragt.yaml: missing"}},
		{"recipient mismatch", func(t *testing.T, dir string) {
			put(t, dir, "host-users/atlas-jkragt.yaml", encryptedTo("age1atlas"))
		}, true, []string{"host-users/atlas-jkragt.yaml: encrypted to other recipients"}},
		{"shadowing catch-all", func(t *testing.T, dir string) {
			data, err := os.ReadFile(filepath.Join(dir, ".sops.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			put(t, dir, ".sops.yaml", strings.Replace(string(data), "creation_rules:\n", "creation_rules:\n"+catchAll, 1))
		}, true, []string{
			"hosts/atlas.yaml: sops would use creation rule 1, which has no path_regex",
			"host-users/atlas-jkragt.yaml: sops would use creation rule 1",
			"hosts/atlas.yaml: encrypted to other recipients",
		}},
		{"file of a removed user", func(t *testing.T, dir string) {
			put(t, dir, "host-users/atlas-gone.yaml", encryptedTo("age1atlas"))
		}, true, []string{"host-users/atlas-gone.yaml: gone is no longer a user of atlas"}},
		{"not pushed", func(t *testing.T, dir string) {
			put(t, dir, "notes", "local")
		}, false, []string{"is not on the upstream"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := pushedEnv(t)
			dir := env.SecretsDir
			tt.change(t, dir)
			gitIn(t, dir, "add", "-A", ".")
			gitIn(t, dir, "commit", "-q", "-m", tt.name)
			if tt.push {
				gitIn(t, dir, "push", "-q")
			}
			env.State.SecretsCommit = gitIn(t, dir, "rev-parse", "HEAD")

			err := pushedSecrets(context.Background(), env)
			if err == nil {
				t.Fatal("no error")
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("err = %v\nwant %q in it", err, w)
				}
			}
		})
	}
}
