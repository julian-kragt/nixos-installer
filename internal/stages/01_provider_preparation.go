package stages

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"installer/internal/luks"
	"installer/internal/machine"
	"installer/internal/nix"
	"installer/internal/pipeline"
	"installer/internal/runner"
)

// providerPreparation makes sure everything is in place before the target is
// wiped, gets the operator's confirmation and delivers the LUKS passphrase. It
// only reads from the target.
func providerPreparation(ctx context.Context, env *pipeline.Env) error {
	var info nix.HostInfo
	err := runner.RunSteps(ctx,
		runner.Step{Title: "Read host data", Fn: func(ctx context.Context) (err error) {
			info, err = configFlake(env).HostInfo(ctx, env.Host)
			return err
		}},
		runner.Step{Title: "Check config repository", Fn: func(ctx context.Context) error {
			// Only pushed config is installed in Phase 3. The installer changes these two itself.
			return configRepo(env).RequireCommitted(ctx, info.HardwareConfigPath, "flake.lock")
		}},
		runner.Step{Title: "Check SSH access to the installed system", Fn: func(ctx context.Context) error {
			return checkBootstrapKeys(ctx, env, info.RootAuthorizedKeys)
		}},
		runner.Step{Title: "Check bootstrap system", Fn: func(ctx context.Context) error {
			return configFlake(env).CheckBootstrapSystem(ctx, env.Host)
		}},
		runner.Step{Title: "Check the host's disks", Fn: func(ctx context.Context) error {
			return checkDisks(ctx, env.Remote, env.Host, info.Disks)
		}},
		runner.Step{Title: "Check target", Fn: func(ctx context.Context) error {
			return checkTarget(ctx, env.Remote)
		}},
	)
	if err != nil {
		return err
	}

	if err := confirmInstall(ctx, env.Remote, env.Host, info.Disks); err != nil {
		return err
	}
	return runner.Run(ctx, "Deliver LUKS passphrase", func(ctx context.Context) error {
		return luks.AskAndSend(ctx, env.Remote)
	})
}

// checkBootstrapKeys fails when none of the provider's agent keys is among
// authorized, root's keys on the installed system: password login is off there,
// so the installer could not reach it after the base install.
func checkBootstrapKeys(ctx context.Context, env *pipeline.Env, authorized []string) error {
	agent, err := env.Local.AgentKeys(ctx)
	if err != nil {
		return fmt.Errorf("no keys in the ssh-agent (load one with ssh-add): %w", err)
	}
	if !machine.KeyAuthorized(agent, authorized) {
		return fmt.Errorf("none of the ssh-agent keys is in root's authorized keys of the %s bootstrap system; "+
			"the installed system would be unreachable", env.Host)
	}
	return nil
}

// checkDisks fails unless every device of the host's disko layout, by disk name,
// is a block device on target. The operator sets the devices in the config.
func checkDisks(ctx context.Context, target machine.Machine, host string, devices map[string]string) error {
	for _, name := range slices.Sorted(maps.Keys(devices)) {
		if _, err := target.Run(ctx, "test", "-b", devices[name]); err != nil {
			return fmt.Errorf("%s (disk %s of %s) is not a block device on the target; set the device in the config, then commit and push",
				devices[name], name, host)
		}
	}
	return nil
}

// checkTarget checks what the base install needs on the target ISO.
func checkTarget(ctx context.Context, target machine.Machine) error {
	for _, tool := range []string{"nix", "lsblk", "curl"} {
		if _, err := target.Run(ctx, "sh", "-c", `command -v "$1" >/dev/null`, "sh", tool); err != nil {
			return fmt.Errorf("%s is missing on the target", tool)
		}
	}
	_, err := target.Run(ctx, "curl", "-fsS", "--max-time", "10", "-o", "/dev/null", "https://cache.nixos.org/nix-cache-info")
	if err != nil {
		return fmt.Errorf("target cannot reach cache.nixos.org: %w", err)
	}
	return nil
}

// confirmInstall shows what is about to be wiped and requires the operator to
// type "install".
func confirmInstall(ctx context.Context, target machine.Machine, host string, devices map[string]string) error {
	name, err := target.Run(ctx, "hostname")
	if err != nil {
		return err
	}
	disks, err := target.Run(ctx, "lsblk", "-d", "-o", "NAME,SIZE,MODEL,TYPE")
	if err != nil {
		return err
	}
	runner.Info(ctx, fmt.Sprintf("Target %s will install %s", strings.TrimSpace(name), host))
	for _, line := range strings.Split(strings.TrimSpace(disks), "\n") {
		runner.Info(ctx, line)
	}
	wiped := strings.Join(slices.Sorted(maps.Values(devices)), ", ")
	return runner.RequireTyped(ctx, fmt.Sprintf("Everything on %s will be erased. Type 'install' to continue: ", wiped), "install",
		errors.New("confirmation required: run the installer in a terminal"),
		errors.New("aborted: nothing was changed on the target"))
}
