package stages

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"installer/internal/commands/nixosrebuild"
	"installer/internal/commands/systemd"
	"installer/internal/machine"
	"installer/internal/pipeline"
	"installer/internal/runner"
)

// switchReconnectTimeout is how long the target may take to answer again when
// the connection dropped during the switch.
const switchReconnectTimeout = 10 * time.Minute

// rebuildPlan is what 3.2 reads from the config at ConfigRev before it
// touches the target.
type rebuildPlan struct {
	flake         string   // git+ssh flake URL of the host at ConfigRev
	urls          []string // the repositories the target fetches: config and nix-secrets
	system        string   // store path of the host's full system
	users         []string // the host's normal users, sorted
	passwordUsers []string // the users whose password comes from sops
}

// rebuild switches the target from the bootstrap system to the host's full
// system at ConfigRev, fetched from GitHub with the provider's agent, and
// checks that it came up right. A connection that drops during the switch is
// waited out.
func rebuild(ctx context.Context, env *pipeline.Env) error {
	if env.State.ConfigRev == "" {
		return errors.New("no config revision is recorded; run stage 3.1 first")
	}
	var plan rebuildPlan
	return runner.RunSteps(ctx,
		runner.Step{Title: fmt.Sprintf("Read config at %.7s", env.State.ConfigRev), Fn: func(ctx context.Context) (err error) {
			plan, err = readRebuildPlan(ctx, env)
			return err
		}},
		runner.Step{Title: "Check GitHub access", Fn: func(ctx context.Context) error {
			return checkForwardedAccess(ctx, env, plan.urls)
		}},
		runner.Step{Title: "Rebuild", Fn: func(ctx context.Context) error {
			return switchSystem(ctx, env, plan.flake)
		}},
		runner.Step{Title: "Check host key", Fn: func(ctx context.Context) error {
			return machine.Access{Remote: env.Remote, Addr: env.Target, Pin: env.Pin, Installed: true}.CheckHostKey(ctx)
		}},
		runner.Step{Title: "Check the system", Fn: func(ctx context.Context) error {
			got, err := nixosrebuild.CurrentSystem(ctx, env.Remote)
			if err != nil {
				return err
			}
			if got != plan.system {
				return fmt.Errorf("%s runs %s, not %s, the system of %.7s\nsee journalctl -u nixos-rebuild-switch-to-configuration on the target",
					env.Host, got, plan.system, env.State.ConfigRev)
			}
			return nil
		}},
		runner.Step{Title: "Validate", Fn: func(ctx context.Context) error {
			return validateSystem(ctx, env, plan)
		}},
	)
}

// readRebuildPlan reads the flake URL, the repositories behind it, the system
// and the users from the config at ConfigRev, on the provider.
func readRebuildPlan(ctx context.Context, env *pipeline.Env) (rebuildPlan, error) {
	var plan rebuildPlan
	// The flake URL names the repository, so the flake must be at its top.
	if prefix, err := configRepo(env).Prefix(ctx); err != nil || prefix != "" {
		return plan, fmt.Errorf("%s is not the top of a git repository; the target fetches the config from its origin's top", env.ConfigDir)
	}
	configURL, err := configRepo(env).OriginURL(ctx)
	if err != nil {
		return plan, err
	}
	at := configFlake(env).At(env.State.ConfigRev)
	secretsURL, err := at.InputURL(ctx, secretsInput)
	if err != nil {
		return plan, err
	}
	users, err := hostUsersAt(ctx, env, env.State.ConfigRev)
	if err != nil {
		return plan, err
	}
	passwordUsers, err := at.PasswordUsers(ctx, env.Host)
	if err != nil {
		return plan, err
	}
	system, err := at.Toplevel(ctx, env.Host)
	if err != nil {
		return plan, err
	}
	return rebuildPlan{
		flake:         "git+" + configURL + "?rev=" + env.State.ConfigRev + "#" + env.Host,
		urls:          []string{configURL, secretsURL},
		system:        system,
		users:         users,
		passwordUsers: passwordUsers,
	}, nil
}

// checkForwardedAccess fails unless the provider's agent holds keys, the
// target gets them forwarded and reaches each of urls with them, so missing
// GitHub access fails before the switch, not halfway.
func checkForwardedAccess(ctx context.Context, env *pipeline.Env, urls []string) error {
	if _, err := env.Local.AgentKeys(ctx); err != nil {
		return fmt.Errorf("the provider's ssh-agent holds no keys (%w); the target fetches the config and nix-secrets through it: ssh-add", err)
	}
	if _, err := env.Remote.WithAgent().Run(ctx, "ssh-add", "-l"); err != nil {
		return fmt.Errorf("the provider's agent does not reach the target: %w", err)
	}
	for _, url := range urls {
		if _, err := env.Remote.WithAgent().Run(ctx, "git", "ls-remote", url, "HEAD"); err != nil {
			return fmt.Errorf("the target cannot read %s with the provider's agent: %w", url, err)
		}
	}
	return nil
}

// switchSystem runs nixos-rebuild switch on the target, and waits for the
// switch to end there when the connection drops meanwhile.
func switchSystem(ctx context.Context, env *pipeline.Env, flake string) error {
	err := nixosrebuild.Switch(ctx, env.Remote.WithAgent(), flake)
	if machine.LostConnection(err) {
		runner.Warn(ctx, fmt.Sprintf("The connection dropped; waiting for %s to come back at %s", env.Host, env.Target))
		return nixosrebuild.AwaitSwitch(ctx, env.Remote, switchReconnectTimeout)
	}
	if err != nil {
		return fmt.Errorf("%w\nif the config is wrong: fix it, commit and push, remove \"3.1\" from Completed in %s, then run the installer again",
			err, env.State.Path())
	}
	return nil
}

// validateSystem checks that no unit failed, every password from sops is in
// place and no user's Home Manager failed, and logs the NixOS version.
func validateSystem(ctx context.Context, env *pipeline.Env, plan rebuildPlan) error {
	steps := []runner.Step{{Title: "Check units", Fn: func(ctx context.Context) error {
		failed, err := systemd.FailedUnits(ctx, env.Remote)
		if err != nil {
			return err
		}
		if len(failed) > 0 {
			return fmt.Errorf("failed units on %s: %s\nsee journalctl -u <unit> on the target", env.Host, strings.Join(failed, ", "))
		}
		return nil
	}}}
	for _, user := range plan.passwordUsers {
		file := "/run/secrets-for-users/passwords/" + user
		steps = append(steps, runner.Step{Title: "Check password of " + user, Fn: func(ctx context.Context) error {
			if _, err := env.Remote.Run(ctx, "test", "-e", file); err != nil {
				return fmt.Errorf("%s is missing: sops-nix did not decrypt the password of %s: %w", file, user, err)
			}
			return nil
		}})
	}
	for _, user := range plan.users {
		unit := "home-manager-" + user + ".service"
		steps = append(steps, runner.Step{Title: "Check Home Manager of " + user, Fn: func(ctx context.Context) error {
			failed, err := systemd.IsFailed(ctx, env.Remote, unit)
			if err != nil {
				return err
			}
			if failed {
				return fmt.Errorf("%s failed; see journalctl -u %s on the target", unit, unit)
			}
			return nil
		}})
	}
	if err := runner.Parallel(ctx, steps...); err != nil {
		return err
	}
	version, err := env.Remote.Run(ctx, "nixos-version")
	if err != nil {
		return err
	}
	runner.Info(ctx, "nixos-version: "+strings.TrimSpace(version))
	return nil
}
