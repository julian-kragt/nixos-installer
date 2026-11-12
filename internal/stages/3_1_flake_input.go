package stages

import (
	"context"
	"errors"
	"fmt"

	"installer/internal/pipeline"
	"installer/internal/runner"
)

// secretsInput is the config's flake input that holds nix-secrets.
const secretsInput = "nix-secrets"

// flakeInput locks the config's nix-secrets input at the commit 2.4 pushed,
// then commits and pushes the lock file, so the target builds a revision that
// is on the remote. A failure before the push leaves the config as it was.
func flakeInput(ctx context.Context, env *pipeline.Env) error {
	if env.State.SecretsCommit == "" {
		return errors.New("no nix-secrets commit is recorded; run the secrets phase first")
	}
	var base string
	err := runner.Run(ctx, "Check config repository", func(ctx context.Context) error {
		// flake.lock too must be committed, so a failure can put it back.
		if err := configRepo(env).RequireCommitted(ctx, func(string) bool { return false }); err != nil {
			return err
		}
		if err := configRepo(env).RequireInSync(ctx); err != nil {
			return err
		}
		if err := configRepo(env).CheckPush(ctx); err != nil {
			return err
		}
		var err error
		base, err = configRepo(env).Head(ctx)
		return err
	})
	if err != nil {
		return err
	}

	err = runner.Run(ctx, "Lock nix-secrets", func(ctx context.Context) error {
		if err := lockSecrets(ctx, env); err != nil {
			if reset := configRepo(env).ResetTo(context.WithoutCancel(ctx), base, "flake.lock"); reset != nil {
				return fmt.Errorf("%w\nputting %s back at %s failed: %v", err, env.ConfigDir, base, reset)
			}
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}

	err = runner.Run(ctx, "Confirm push", func(ctx context.Context) error {
		return configRepo(env).RequireInSync(ctx)
	})
	if err != nil {
		return err
	}
	head, err := configRepo(env).Head(ctx)
	if err != nil {
		return err
	}
	env.State.ConfigRev = head
	runner.Info(ctx, "nixos-config is at "+head)
	return nil
}

// lockSecrets updates the nix-secrets input, requires it at SecretsCommit,
// and commits and pushes the lock file.
func lockSecrets(ctx context.Context, env *pipeline.Env) error {
	if err := configFlake(env).UpdateInput(ctx, secretsInput); err != nil {
		return err
	}
	rev, err := configFlake(env).LockedRev(secretsInput)
	if err != nil {
		return err
	}
	if rev != env.State.SecretsCommit {
		return fmt.Errorf("the config's %s input now locks %s, not %s, which the secrets phase pushed: "+
			"its branch moved since, or the input's URL is another repository than %s\n"+
			"git -C %s pull, remove \"2.4\" from Completed in %s, then run the installer again",
			secretsInput, rev, env.State.SecretsCommit, env.SecretsDir, env.SecretsDir, env.State.Path())
	}
	message := fmt.Sprintf("chore(%s): lock nix-secrets at %.7s", env.Host, rev)
	if _, err := configRepo(env).Commit(ctx, message, "flake.lock"); err != nil {
		return err
	}
	return configRepo(env).Push(ctx)
}

// flakeInputOutdated asks for 3.1 again when no config revision is recorded
// or the config does not lock the recorded nix-secrets commit, as after 2.4
// ran again and recorded a new one.
func flakeInputOutdated(_ context.Context, env *pipeline.Env) (string, error) {
	if env.State.ConfigRev == "" {
		return "no config revision is recorded", nil
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
