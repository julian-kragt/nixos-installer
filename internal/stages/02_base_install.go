package stages

import (
	"context"
	"fmt"
	"time"

	"installer/internal/luks"
	"installer/internal/machine"
	"installer/internal/nix"
	"installer/internal/nixosanywhere"
	"installer/internal/pipeline"
	"installer/internal/runner"
)

// sshWaitTimeout is how long the first boot may take, including the operator
// typing the LUKS passphrase at the console.
const sshWaitTimeout = 15 * time.Minute

// baseInstall installs the host's bootstrap system on the target, waits for it
// to come up and pins its host key. A target that already runs the system skips
// the install, so a run interrupted after it picks up from there.
func baseInstall(ctx context.Context, env *pipeline.Env) error {
	var info nix.HostInfo
	var kind nixosanywhere.TargetKind
	err := runner.RunSteps(ctx,
		runner.Step{Title: "Read host data", Fn: func(ctx context.Context) (err error) {
			info, err = configFlake(env).HostInfo(ctx, env.Host)
			return err
		}},
		runner.Step{Title: "Check what the target runs", Fn: func(ctx context.Context) (err error) {
			kind, err = nixosanywhere.Inspect(ctx, env.Remote, env.Host)
			return err
		}},
	)
	if err != nil {
		return err
	}

	switch kind {
	case nixosanywhere.TargetInstalled:
		runner.Info(ctx, fmt.Sprintf("The target already runs %s; boot the NixOS ISO to install it again", env.Host))
	case nixosanywhere.TargetISO:
		if err := installFromISO(ctx, env, info.HardwareConfigPath); err != nil {
			return err
		}
	default:
		// nixos-anywhere would kexec, which empties /tmp and so loses the passphrase.
		return fmt.Errorf("the target runs neither the NixOS ISO nor %s; boot it from the NixOS ISO", env.Host)
	}

	return runner.Run(ctx, "Commit hardware config", func(ctx context.Context) error {
		message := fmt.Sprintf("chore(%s): update generated hardware config", env.Host)
		_, err := configRepo(env).Commit(ctx, message, info.HardwareConfigPath)
		return err
	})
}

// installFromISO runs nixos-anywhere on the ISO and waits for the first boot.
func installFromISO(ctx context.Context, env *pipeline.Env, hardwareConfigPath string) error {
	return runner.RunSteps(ctx,
		runner.Step{Title: "Check LUKS passphrase", Fn: func(ctx context.Context) error {
			return luks.Ensure(ctx, env.Remote)
		}},
		runner.Step{Title: "Run nixos-anywhere", Fn: func(ctx context.Context) error {
			return nixosanywhere.Install(ctx, configRepo(env), env.Host, env.Target, hardwareConfigPath)
		}},
		runner.Step{Title: "Wait for the installed system", Fn: func(ctx context.Context) error {
			return awaitInstalledSystem(ctx, env)
		}},
	)
}

// awaitInstalledSystem waits for the first boot, pins the new host key and
// checks that the system behind it is the host.
func awaitInstalledSystem(ctx context.Context, env *pipeline.Env) error {
	old, err := env.Pin.Load()
	if err != nil {
		return err
	}
	runner.Warn(ctx, fmt.Sprintf("Type the LUKS passphrase at the console of %s when it asks", env.Host))
	key, err := machine.WaitForNewHostKey(ctx, env.Target, old, sshWaitTimeout)
	if err != nil {
		return err
	}
	if err := env.Pin.Save(key); err != nil {
		return err
	}
	runner.Info(ctx, "Pinned the installed system's host key: "+machine.Fingerprint(key))

	kind, err := nixosanywhere.Inspect(ctx, env.Remote, env.Host)
	if err != nil {
		return fmt.Errorf("log in to the installed system: %w", err)
	}
	if kind != nixosanywhere.TargetInstalled {
		return fmt.Errorf("the system that came up at %s is not %s", env.Target, env.Host)
	}
	return nil
}
