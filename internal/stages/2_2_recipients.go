package stages

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"installer/internal/pipeline"
	"installer/internal/runner"
	"installer/internal/secrets"
)

// recipients makes the nix-secrets clone's .sops.yaml say exactly who decrypts
// the host's files: the host, and each user's own key on it. Files of users who
// are no longer on the host are deleted; 2.4 commits that. It changes nothing
// while a rule comes before one of the host's files' own rule.
func recipients(ctx context.Context, env *pipeline.Env) error {
	users, err := hostUsers(ctx, env)
	if err != nil {
		return err
	}
	want := secrets.Host{Name: env.Host, Recipient: env.State.HostRecipient, Users: map[string]string{}}
	for _, u := range users {
		r, ok := env.State.UserRecipients[u]
		if !ok {
			return fmt.Errorf("no age key is recorded for %s", u)
		}
		want.Users[u] = r
	}

	path := filepath.Join(env.SecretsDir, secrets.ConfigFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	cfg, err := secrets.ParseConfig(data)
	if err != nil {
		return err
	}
	if err := checkRuleOrder(env, cfg, users); err != nil {
		return err
	}
	stale, err := secrets.Stale(env.SecretsDir, env.Host, users)
	if err != nil {
		return err
	}
	if err := confirmReplacements(ctx, cfg.Replacements(want)); err != nil {
		return err
	}
	for _, u := range cfg.Apply(want) {
		runner.Info(ctx, "Removed the recipient of "+u+", who is no longer on "+env.Host)
	}
	out, err := cfg.Marshal()
	if err != nil {
		return err
	}
	if !bytes.Equal(out, data) {
		if err := os.WriteFile(path, out, 0o644); err != nil {
			return err
		}
		runner.Info(ctx, "Updated "+secrets.ConfigFile)
	}

	for _, f := range stale {
		if err := os.Remove(filepath.Join(env.SecretsDir, f)); err != nil {
			return err
		}
		runner.Info(ctx, "Deleted "+f)
	}
	return nil
}

// recipientsOutdated asks for 2.2 again, and so 2.3 to 2.5, when 2.4 pushed a
// commit that 2.5 did not pass: those stages fix what they can in the clone,
// push it and check again.
func recipientsOutdated(_ context.Context, env *pipeline.Env) (string, error) {
	if env.State.IsCompleted(stageCommitPush) && !env.State.IsCompleted(stagePushedSecrets) {
		return fmt.Sprintf("the pushed nix-secrets commit %.7s has not passed stage %s", env.State.SecretsCommit, stagePushedSecrets), nil
	}
	return "", nil
}

// checkRuleOrder fails unless each of the host's files that has a rule of its
// own is encrypted with it: sops uses the first rule that matches a file, and
// a broader one before it would give the file other recipients. A file
// without a rule gets one from Apply, before any rule that matches it. Other
// hosts' files are not this install's business.
func checkRuleOrder(env *pipeline.Env, cfg *secrets.Config, users []string) error {
	files := slices.DeleteFunc(secrets.Files(env.Host, users), func(f string) bool { return !cfg.HasRule(f) })
	shadowed, err := cfg.Shadowed(files)
	if err != nil || len(shadowed) == 0 {
		return err
	}
	lines := make([]string, 0, len(shadowed))
	for _, s := range shadowed {
		lines = append(lines, "  "+s.String())
	}
	return fmt.Errorf("%s has rules that come before a secret file's own rule:\n%s\n"+
		"sops uses the first rule that matches a file; put each file's own rule first in %s, then run the installer again",
		secrets.ConfigFile, strings.Join(lines, "\n"), filepath.Join(env.SecretsDir, secrets.ConfigFile))
}

// confirmReplacements shows the recipients that change and requires the
// operator to type "replace". After a reinstall the old keys are gone, so
// nobody can read the files encrypted to them any more.
func confirmReplacements(ctx context.Context, replaced []secrets.Replaced) error {
	if len(replaced) == 0 {
		return nil
	}
	for _, r := range replaced {
		runner.Info(ctx, fmt.Sprintf("&%s is %s in %s, the target has %s", r.Anchor, r.Old, secrets.ConfigFile, r.New))
	}
	runner.Info(ctx, "Files encrypted to the old keys are written again in 2.3, with values you type")
	return runner.RequireTyped(ctx, "Type 'replace' to replace these recipients: ", "replace",
		errors.New("confirmation required: run the installer in a terminal"),
		errors.New("aborted: "+secrets.ConfigFile+" was not changed"))
}
