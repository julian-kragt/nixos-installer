package stages

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/julian-kragt/nixos-installer/internal/machine"
	"github.com/julian-kragt/nixos-installer/internal/pipeline"
	"github.com/julian-kragt/nixos-installer/internal/secrets"
)

// referenceLayoutJSON is secrets.ReferenceLayout as installerSecretsData prints it.
const referenceLayoutJSON = `{"hostPath":"hosts","hostTemplatePath":"templates/host.yaml","hostUserPath":"host-users",` +
	`"hostUserTemplatePath":"templates/user.yaml","passwordTemplate":"templates/user.yaml",` +
	`"sharedUserPath":null,"sharedUserTemplatePath":null,"userMode":"per-host"}`

// encrypted is a stand-in for a sops file; the stages only look at its shape.
const encrypted = "hashedPassword: ENC[AES256_GCM,data:abc,type:str]\nsops:\n  age:\n    - recipient: age1myhost\n"

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

// secretsEnv returns an Env for myhost with user jdoe (from a fake nix) and a
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
	// nix-secrets' layout for installerSecretsData, the host's entry otherwise.
	put(t, bin, "nix", `#!/bin/sh
case "$*" in
*installerSecretsData*) printf '%s' '`+referenceLayoutJSON+`' ;;
*) printf '%s' '{"users":{"jdoe":{"uid":1000,"home":"/home/jdoe"}}}' ;;
esac
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
	put(t, dir, "flake.nix", "{ }\n")
	put(t, dir, ".sops.yaml", "keys:\n  - &host_other age1other\ncreation_rules:\n  - path_regex: ^hosts/other\\.yaml$\n    key_groups:\n      - age:\n          - *host_other\n")
	put(t, dir, "host-users/myhost-gone.yaml", encrypted)
	put(t, dir, "host-users/other-jdoe.yaml", encrypted)
	gitIn(t, top, "add", ".")
	gitIn(t, top, "commit", "-q", "-m", "init")
	gitIn(t, top, "remote", "add", "origin", remote)
	gitIn(t, top, "push", "-q", "-u", "origin", "main")

	env := &pipeline.Env{
		Inputs: pipeline.Inputs{Host: "myhost", ConfigDir: t.TempDir(), SecretsDir: dir},
		State:  pipeline.NewState(t.TempDir(), "myhost"),
		Local:  machine.Local(),
	}
	env.State.HostRecipient, env.State.UserRecipients = "age1myhost", map[string]string{"jdoe": "age1jdoe"}
	layout := secrets.ReferenceLayout
	env.State.SecretsLayout = &layout
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
	if _, err := os.Stat(filepath.Join(dir, "host-users/myhost-gone.yaml")); !os.IsNotExist(err) {
		t.Errorf("file of the removed user: %v, want it deleted", err)
	}
	// What 2.3 would have encrypted.
	put(t, dir, "hosts/myhost.yaml", encrypted)
	put(t, dir, "host-users/myhost-jdoe.yaml", encrypted)

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
	if files := gitIn(t, dir, "ls-files"); strings.Contains(files, "myhost-gone") || !strings.Contains(files, "other-jdoe") {
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
	put(t, env.SecretsDir, "hosts/myhost.yaml", encrypted)
	put(t, env.SecretsDir, "host-users/myhost-jdoe.yaml", encrypted)
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
	put(t, env.SecretsDir, "hosts/myhost.yaml", "placeholder: unused\n")
	put(t, env.SecretsDir, "host-users/myhost-jdoe.yaml", encrypted)
	start := gitIn(t, env.SecretsDir, "rev-parse", "HEAD")

	err := commitPush(ctx, env)
	if err == nil || !strings.Contains(err.Error(), "hosts/myhost.yaml") {
		t.Fatalf("err = %v, want the plaintext file named", err)
	}
	if head := gitIn(t, env.SecretsDir, "rev-parse", "HEAD"); head != start {
		t.Error("committed despite the plaintext")
	}
}

// A rule before an existing rule of one of the host's files, here one
// without path_regex, fails 2.2 before it changes anything. Other hosts'
// files are not checked.
func TestRecipientsRefusesAShadowingRule(t *testing.T) {
	env, _ := secretsEnv(t)
	path := filepath.Join(env.SecretsDir, ".sops.yaml")
	put(t, env.SecretsDir, ".sops.yaml", "keys:\n  - &host_other age1other\n  - &host_myhost age1myhost\ncreation_rules:\n"+
		"  - key_groups:\n      - age:\n          - *host_other\n"+
		"  - path_regex: ^hosts/myhost\\.yaml$\n    key_groups:\n      - age:\n          - *host_myhost\n"+
		"  - path_regex: ^hosts/other\\.yaml$\n    key_groups:\n      - age:\n          - *host_other\n")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	err = recipients(context.Background(), env)
	if err == nil {
		t.Fatal("2.2 accepted a catch-all before myhost' rule")
	}
	if !strings.Contains(err.Error(), "hosts/myhost.yaml") {
		t.Errorf("err = %v, want hosts/myhost.yaml named", err)
	}
	// jdoe's file has no rule yet, the others are not myhost' files.
	for _, f := range []string{"host-users/myhost-jdoe.yaml", "hosts/other.yaml", "host-users/other-jdoe.yaml", "myhost-gone"} {
		if strings.Contains(err.Error(), f) {
			t.Errorf("err = %v names %s", err, f)
		}
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Errorf(".sops.yaml changed:\n%s", after)
	}
	if _, err := os.Stat(filepath.Join(env.SecretsDir, "host-users/myhost-gone.yaml")); err != nil {
		t.Errorf("file of the removed user: %v, want it kept", err)
	}
}

// A new host's rules go before a trailing catch-all, so sops uses them.
func TestRecipientsAddsRulesBeforeACatchAll(t *testing.T) {
	env, _ := secretsEnv(t)
	put(t, env.SecretsDir, ".sops.yaml", "keys:\n  - &host_other age1other\ncreation_rules:\n"+
		"  - path_regex: ^hosts/other\\.yaml$\n    key_groups:\n      - age:\n          - *host_other\n"+
		"  - key_groups:\n      - age:\n          - *host_other\n")
	if err := recipients(context.Background(), env); err != nil {
		t.Fatalf("2.2: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(env.SecretsDir, ".sops.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := secrets.ParseConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string][]string{
		"hosts/myhost.yaml":           {"age1myhost"},
		"host-users/myhost-jdoe.yaml": {"age1myhost", "age1jdoe"},
	} {
		if got, err := cfg.Recipients(file); err != nil || !slices.Equal(got, want) {
			t.Errorf("%s: recipients = %q, %v, want %q", file, got, err, want)
		}
	}
}

// A new rule that would take a file from an operator's rule, here one with an
// unescaped dot, is not added without a typed confirmation; nor is anything
// when a path_regex does not compile.
func TestRecipientsAsksBeforeTakingAFileFromAnotherRule(t *testing.T) {
	for name, tt := range map[string]struct{ rule, wantErr string }{
		"differently written rule": {"  - path_regex: ^hosts/myhost.yaml$\n    key_groups:\n      - age:\n          - *host_other\n", "confirmation required"},
		"broken path_regex":        {"  - path_regex: ^hosts/(\n    key_groups:\n      - age:\n          - *host_other\n", "path_regex"},
	} {
		t.Run(name, func(t *testing.T) {
			env, _ := secretsEnv(t)
			config := "keys:\n  - &host_other age1other\ncreation_rules:\n" + tt.rule
			put(t, env.SecretsDir, ".sops.yaml", config)
			err := recipients(context.Background(), env)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q in it", err, tt.wantErr)
			}
			if after, _ := os.ReadFile(filepath.Join(env.SecretsDir, ".sops.yaml")); string(after) != config {
				t.Errorf(".sops.yaml changed:\n%s", after)
			}
		})
	}
}

func TestSecretsRepositoryRefusesUnrelatedChanges(t *testing.T) {
	env, _ := secretsEnv(t)
	put(t, env.SecretsDir, "host-users/other-jdoe.yaml", "changed")
	if err := secretsRepository(context.Background(), env); err == nil || !strings.Contains(err.Error(), "other-jdoe") {
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
		{"same users", map[string]string{"jdoe": "age1jdoe"}, ""},
		{"user removed from the host", map[string]string{"jdoe": "age1jdoe", "gone": "age1gone"}, "the users of myhost are jdoe, the recorded ones gone, jdoe"},
		{"user added to the host", nil, "the users of myhost are jdoe, the recorded ones none"},
		{"recipient missing", map[string]string{"jdoe": ""}, "no recipient is recorded for jdoe"},
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

// pushedEnv returns an Env after 2.2 to 2.4: myhost' files committed, pushed
// and recorded as SecretsCommit.
func pushedEnv(t *testing.T) *pipeline.Env {
	t.Helper()
	env, _ := secretsEnv(t)
	ctx := context.Background()
	if err := recipients(ctx, env); err != nil {
		t.Fatal(err)
	}
	put(t, env.SecretsDir, "hosts/myhost.yaml", encryptedTo("age1myhost"))
	put(t, env.SecretsDir, "host-users/myhost-jdoe.yaml", encryptedTo("age1myhost", "age1jdoe"))
	if err := commitPush(ctx, env); err != nil {
		t.Fatal(err)
	}
	return env
}

func TestPushedSecretsChecksTheCommitNotTheFiles(t *testing.T) {
	env := pushedEnv(t)
	put(t, env.SecretsDir, "hosts/myhost.yaml", "placeholder: unused\n")
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
			put(t, dir, "hosts/myhost.yaml", "hashedPassword: plain\n")
		}, true, []string{"hosts/myhost.yaml: hashedPassword is not encrypted"}},
		{"missing user file", func(t *testing.T, dir string) {
			gitIn(t, dir, "rm", "-q", "host-users/myhost-jdoe.yaml")
		}, true, []string{"host-users/myhost-jdoe.yaml: missing"}},
		{"recipient mismatch", func(t *testing.T, dir string) {
			put(t, dir, "host-users/myhost-jdoe.yaml", encryptedTo("age1myhost"))
		}, true, []string{"host-users/myhost-jdoe.yaml: encrypted to other recipients"}},
		{"shadowing catch-all", func(t *testing.T, dir string) {
			data, err := os.ReadFile(filepath.Join(dir, ".sops.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			put(t, dir, ".sops.yaml", strings.Replace(string(data), "creation_rules:\n", "creation_rules:\n"+catchAll, 1))
		}, true, []string{
			"hosts/myhost.yaml: sops would use creation rule 1, which has no path_regex",
			"host-users/myhost-jdoe.yaml: sops would use creation rule 1",
			"hosts/myhost.yaml: encrypted to other recipients",
		}},
		{"file of a removed user", func(t *testing.T, dir string) {
			put(t, dir, "host-users/myhost-gone.yaml", encryptedTo("age1myhost"))
		}, true, []string{"host-users/myhost-gone.yaml: gone is no longer a user of myhost"}},
		{"not pushed", func(t *testing.T, dir string) {
			put(t, dir, "notes", "local")
		}, false, []string{"is not on the upstream"}},
		// Without .sops.yaml the checks that need no recipients still run.
		{"no .sops.yaml", func(t *testing.T, dir string) {
			gitIn(t, dir, "rm", "-q", ".sops.yaml")
			put(t, dir, "hosts/myhost.yaml", "hashedPassword: plain\n")
			put(t, dir, "host-users/myhost-gone.yaml", encryptedTo("age1myhost"))
		}, true, []string{
			".sops.yaml: missing",
			"hosts/myhost.yaml: hashedPassword is not encrypted",
			"host-users/myhost-gone.yaml: gone is no longer a user of myhost",
		}},
		{"no rule and plaintext", func(t *testing.T, dir string) {
			data, err := os.ReadFile(filepath.Join(dir, ".sops.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			cfg := strings.Replace(string(data), `^host-users/myhost-jdoe\.yaml$`, `^host-users/nobody\.yaml$`, 1)
			put(t, dir, ".sops.yaml", cfg)
			put(t, dir, "host-users/myhost-jdoe.yaml", "hashedPassword: plain\n")
		}, true, []string{
			"host-users/myhost-jdoe.yaml: hashedPassword is not encrypted; .sops.yaml: no creation rule matches host-users/myhost-jdoe.yaml",
		}},
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

// An error that is not a problem of the commit reopens nothing: the next run
// only runs 2.5 again.
func TestPushedSecretsFailsWithoutAnUpstreamAndReopensNothing(t *testing.T) {
	env := pushedEnv(t)
	env.State.Completed = []string{"2.1", "2.2", "2.3", "2.4"}
	gitIn(t, env.SecretsDir, "branch", "--unset-upstream")
	err := pushedSecrets(context.Background(), env)
	if err == nil || !strings.Contains(err.Error(), "on the upstream") {
		t.Fatalf("err = %v, want the upstream check reported", err)
	}
	if strings.Contains(err.Error(), "the next run repeats") {
		t.Errorf("err = %v, want no repeated stages", err)
	}
	if got := strings.Join(env.State.Completed, ","); got != "2.1,2.2,2.3,2.4" {
		t.Errorf("completed = %s, want 2.1 to 2.4 kept", got)
	}
}

// A commit with a problem reopens 2.1 and every stage after it, so the next
// run checks the clone, fixes it, pushes, locks and deploys again; earlier
// stages stay done.
func TestPushedSecretsReopensTheSecretsPhase(t *testing.T) {
	env := pushedEnv(t)
	env.State.Completed = []string{"1.3", "2.1", "2.2", "2.3", "2.4", "3.1", "3.2", "3.3"}
	dir := env.SecretsDir
	put(t, dir, "hosts/myhost.yaml", "hashedPassword: plain\n")
	gitIn(t, dir, "commit", "-q", "-am", "plain")
	gitIn(t, dir, "push", "-q")
	env.State.SecretsCommit = gitIn(t, dir, "rev-parse", "HEAD")

	err := pushedSecrets(context.Background(), env)
	if err == nil || !strings.Contains(err.Error(), "the next run repeats stage 2.1 and every stage after it") {
		t.Fatalf("err = %v, want the way forward", err)
	}
	saved, err := pipeline.LoadState(filepath.Dir(env.State.Path()), "myhost")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(saved.Completed, ","); got != "1.3" {
		t.Errorf("saved completed = %s, want 1.3", got)
	}
}

// In shared mode, a user who left myhost loses it from their shared file's
// rule: the file stays for the other host that reads it, or goes when
// nobody else does, and 2.4 commits both with myhost's files.
func TestRecipientsThenCommitPushInSharedMode(t *testing.T) {
	env, _ := secretsEnv(t)
	ctx := context.Background()
	dir := env.SecretsDir
	env.State.SecretsLayout = &secrets.Layout{
		UserMode:           secrets.Shared,
		HostPath:           "hosts",
		SharedUserPath:     "users",
		HostTemplate:       "templates/host.yaml",
		SharedUserTemplate: "templates/user.yaml",
	}
	put(t, dir, ".sops.yaml", `keys:
  - &host_myhost age1myhost
  - &user_gone_myhost age1gonemyhost
  - &user_solo_myhost age1solomyhost
  - &host_other age1other
  - &user_gone_other age1goneother
creation_rules:
  - path_regex: ^users/gone\.yaml$
    key_groups:
      - age:
          - *host_myhost
          - *user_gone_myhost
          - *host_other
          - *user_gone_other
  - path_regex: ^users/solo\.yaml$
    key_groups:
      - age:
          - *host_myhost
          - *user_solo_myhost
`)
	put(t, dir, "users/gone.yaml", encrypted)
	put(t, dir, "users/solo.yaml", encrypted)
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "shared")
	gitIn(t, dir, "push", "-q")

	if err := recipients(ctx, env); err != nil {
		t.Fatalf("2.2: %v", err)
	}
	if want := []string{"users/gone.yaml", "users/solo.yaml"}; !slices.Equal(env.State.LeftShared, want) {
		t.Errorf("LeftShared = %q, want %q", env.State.LeftShared, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "users/solo.yaml")); !os.IsNotExist(err) {
		t.Errorf("file nobody reads: %v, want it deleted", err)
	}
	cfg, err := os.ReadFile(filepath.Join(dir, ".sops.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if s := string(cfg); strings.Contains(s, "solo") || strings.Contains(s, "user_gone_myhost") || !strings.Contains(s, "^users/jdoe\\.yaml$") {
		t.Errorf(".sops.yaml:\n%s", s)
	}

	// What 2.3 would have written: myhost's files, and gone's on the other host.
	put(t, dir, "hosts/myhost.yaml", encrypted)
	put(t, dir, "users/jdoe.yaml", encrypted)
	put(t, dir, "users/gone.yaml", strings.Replace(encrypted, "age1myhost", "age1other", 1))
	if err := commitPush(ctx, env); err != nil {
		t.Fatalf("2.4: %v", err)
	}
	if status := gitIn(t, dir, "status", "--porcelain"); status != "" {
		t.Errorf("left uncommitted: %q", status)
	}
	changed := gitIn(t, dir, "show", "--no-renames", "--name-status", "--format=", "HEAD")
	for _, want := range []string{"M\t.sops.yaml", "A\thosts/myhost.yaml", "A\tusers/jdoe.yaml", "M\tusers/gone.yaml", "D\tusers/solo.yaml"} {
		if !strings.Contains(changed, want) {
			t.Errorf("commit lacks %q:\n%s", want, changed)
		}
	}
}
