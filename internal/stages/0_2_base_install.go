package stages

import (
	"context"
	"fmt"
	"time"

	"github.com/julian-kragt/nixos-installer/internal/commands/luks"
	"github.com/julian-kragt/nixos-installer/internal/commands/nix"
	"github.com/julian-kragt/nixos-installer/internal/commands/nixosanywhere"
	"github.com/julian-kragt/nixos-installer/internal/console"
	"github.com/julian-kragt/nixos-installer/internal/machine"
	"github.com/julian-kragt/nixos-installer/internal/pipeline"
	"github.com/julian-kragt/nixos-installer/internal/runner"
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
		if err := installFromISO(ctx, env, info); err != nil {
			return err
		}
	default:
		// nixos-anywhere would kexec, which empties /tmp and so loses the passphrase.
		return fmt.Errorf("the target runs neither the NixOS ISO nor %s; boot it from the NixOS ISO", env.Host)
	}

	return runner.Run(ctx, "Commit and push hardware config", func(ctx context.Context) error {
		message := fmt.Sprintf("chore(%s): update generated hardware config", env.Host)
		if _, err := configRepo(env).Commit(ctx, message, info.HardwareConfigPath); err != nil {
			return err
		}
		// Also without a new commit: a re-run retries a push that failed.
		return configRepo(env).Push(ctx)
	})
}

// installFromISO runs nixos-anywhere on the ISO and waits for the first boot.
func installFromISO(ctx context.Context, env *pipeline.Env, info nix.HostInfo) error {
	return runner.RunSteps(ctx,
		runner.Step{Title: "Check LUKS passphrase", Fn: func(ctx context.Context) error {
			return ensurePassphrase(ctx, env, info.System)
		}},
		runner.Step{Title: "Run nixos-anywhere", Fn: func(ctx context.Context) error {
			return nixosanywhere.Install(ctx, configRepo(env), env.Host, env.Target, info.HardwareConfigPath)
		}},
		runner.Step{Title: "Wait for the installed system", Fn: func(ctx context.Context) error {
			return awaitInstalledSystem(ctx, env)
		}},
	)
}

// ensurePassphrase has the operator type the LUKS passphrase at the console
// again when it is not on the ISO any more, as after a reboot, which also
// removed the console helper.
func ensurePassphrase(ctx context.Context, env *pipeline.Env, system string) error {
	present, err := luks.Present(ctx, env.Remote)
	if err != nil || present {
		return err
	}
	session, err := console.Prepare(ctx, env.Local, env.Remote, env.Host, system)
	if err != nil {
		return err
	}
	return luks.Ask(ctx, session)
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
