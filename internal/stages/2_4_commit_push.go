package stages

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/julian-kragt/nixos-installer/internal/commands/sops"
	"github.com/julian-kragt/nixos-installer/internal/pipeline"
	"github.com/julian-kragt/nixos-installer/internal/runner"
	"github.com/julian-kragt/nixos-installer/internal/secrets"
)

// commitPush publishes the host's secrets as one commit in nix-secrets and
// records the commit the system is built from: the host's files, the shared
// files its users left, and the deletions of 2.2. Every file is checked to be
// encrypted first, so plaintext is never committed.
func commitPush(ctx context.Context, env *pipeline.Env) error {
	users, err := hostUsers(ctx, env)
	if err != nil {
		return err
	}
	layout, err := secretsLayout(env)
	if err != nil {
		return err
	}
	files := layout.Files(env.Host, users)
	// The shared files users left that other hosts still read; those nobody
	// reads are deleted, below.
	for _, f := range env.State.LeftShared {
		if _, err := os.Stat(filepath.Join(env.SecretsDir, f)); err == nil && !slices.Contains(files, f) {
			files = append(files, f)
		}
	}
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(env.SecretsDir, f))
		if err != nil {
			return err
		}
		if err := sops.CheckCiphertext(data); err != nil {
			return fmt.Errorf("%s: %w; nothing was committed", f, err)
		}
	}
	// The files of removed users, deleted by 2.2.
	deleted, err := secretsRepo(env).Deleted(ctx)
	if err != nil {
		return err
	}
	for _, p := range deleted {
		if _, ok := layout.FileUser(env.Host, p); ok || slices.Contains(env.State.LeftShared, p) {
			files = append(files, p)
		}
	}

	paths := slices.Concat([]string{secrets.ConfigFile}, files)
	committed, err := secretsRepo(env).Commit(ctx, fmt.Sprintf("chore(%s): update secrets", env.Host), paths...)
	if err != nil {
		return err
	}
	if !committed {
		runner.Info(ctx, "Nothing changed in nix-secrets")
	}
	// Also without a new commit: a re-run retries a push that failed.
	if err := secretsRepo(env).Push(ctx); err != nil {
		return fmt.Errorf("%w\nif the remote has new commits: git -C %s pull --rebase, then run the installer again", err, env.SecretsDir)
	}
	head, err := secretsRepo(env).Head(ctx)
	if err != nil {
		return err
	}
	env.State.SecretsCommit = head
	runner.Info(ctx, "nix-secrets is at "+head)
	return nil
}

// commitPushOutdated asks for 2.4 again, and so 2.5 and 3.1 on, when 3.1 has
// locked SecretsCommit into the config and the config now locks another
// commit, as after another host's install: 2.4 then records the commit the
// clone is at, which 3.1 can lock.
func commitPushOutdated(_ context.Context, env *pipeline.Env) (string, error) {
	if !env.State.IsCompleted(stageFlakeInput) {
		return "", nil
	}
	rev, err := configFlake(env).LockedRev(secretsInput)
	if err != nil {
		return "", err
	}
	if rev != env.State.SecretsCommit {
		return fmt.Sprintf("the config locks nix-secrets at %.7s, not at %.7s", rev, env.State.SecretsCommit), nil
	}
	return "", nil
}
