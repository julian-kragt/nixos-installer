package stages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"installer/internal/commands/hardwareconfig"
	"installer/internal/commands/luks"
	"installer/internal/commands/nix"
	"installer/internal/console"
	"installer/internal/machine"
	"installer/internal/pipeline"
	"installer/internal/runner"
)

// providerPreparation makes sure everything is in place before the target is
// wiped, gets the operator's confirmation and has the operator type the LUKS
// passphrase at the target's console. On a reinstall, what is saved for the
// host is replaced only then. Besides the console helper, it only
// reads from the target. A host without a hardware config gets one here,
// because nothing about the host can be read until it exists.
func providerPreparation(ctx context.Context, env *pipeline.Env) error {
	var hardwareConfigPath string
	var info nix.HostInfo
	var session console.Session
	err := runner.RunSteps(ctx,
		runner.Step{Title: "Find hardware config", Fn: func(ctx context.Context) (err error) {
			hardwareConfigPath, err = configFlake(env).HardwareConfigPath(ctx, env.Host)
			return err
		}},
		runner.Step{Title: "Check config repository", Fn: func(ctx context.Context) error {
			// Only pushed config is installed in Phase 3. The installer writes
			// the hardware config itself; flake.lock it changes only in 3.1,
			// which needs it committed, so a change to it fails here, before
			// the target is wiped.
			if err := configRepo(env).RequireCommitted(ctx, func(p string) bool { return p == hardwareConfigPath }); err != nil {
				return err
			}
			return configRepo(env).RequireInSync(ctx)
		}},
		runner.Step{Title: "Generate hardware config", Fn: func(ctx context.Context) error {
			return hardwareconfig.Ensure(ctx, configRepo(env), env.Remote, hardwareConfigPath, env.GenerateHardware)
		}},
		runner.Step{Title: "Read host data", Fn: func(ctx context.Context) (err error) {
			info, err = configFlake(env).HostInfo(ctx, env.Host)
			return err
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
		runner.Step{Title: "Install console helper", Fn: func(ctx context.Context) (err error) {
			session, err = console.Prepare(ctx, env.Local, env.Remote, env.Host, info.System)
			return err
		}},
	)
	if err != nil {
		return err
	}

	if err := confirmInstall(ctx, env.Remote, env.Host, info.Disks); err != nil {
		return err
	}
	err = runner.Run(ctx, "LUKS passphrase at the console", func(ctx context.Context) error {
		return luks.Ask(ctx, session)
	})
	if err != nil {
		return err
	}
	// Only now, with the wipe confirmed, is what is saved for the host replaced.
	if env.Reinstall {
		if err := env.ConfirmReinstall(); err != nil {
			return err
		}
		runner.Info(ctx, "Deleted the saved state and pinned host key of "+env.Host)
	}
	return nil
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
		if !target.HasCommand(ctx, tool) {
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
	disks, err := target.Run(ctx, "lsblk", "-d", "-J", "-o", "PATH,NAME,SIZE,MODEL,TYPE")
	if err != nil {
		return err
	}
	// The config may name a device by a /dev/disk/by-id/ link; lsblk lists /dev/<name>.
	var erased []string
	for _, device := range devices {
		path, err := target.Run(ctx, "readlink", "-f", device)
		if err != nil {
			return err
		}
		erased = append(erased, strings.TrimSpace(path))
	}
	rows, err := diskRows(disks, erased)
	if err != nil {
		return err
	}
	runner.Info(ctx, fmt.Sprintf("Target %s will install %s", strings.TrimSpace(name), host))
	for _, row := range rows {
		runner.Detail(ctx, row)
	}
	wiped := strings.Join(slices.Sorted(maps.Values(devices)), ", ")
	return runner.RequireTyped(ctx, fmt.Sprintf("Everything on %s will be erased. Type 'install' to continue: ", wiped), "install",
		errors.New("confirmation required: run the installer in a terminal"),
		errors.New("aborted: nothing was changed on the target"))
}

// diskRows turns the JSON of lsblk into one aligned row per disk, marking the
// disks whose path is in erased. Loop devices and the like are left out.
func diskRows(lsblkJSON string, erased []string) ([]string, error) {
	var out struct {
		Blockdevices []struct {
			Path  string `json:"path"`
			Name  string `json:"name"`
			Size  string `json:"size"`
			Model string `json:"model"`
			Type  string `json:"type"`
		} `json:"blockdevices"`
	}
	if err := json.Unmarshal([]byte(lsblkJSON), &out); err != nil {
		return nil, fmt.Errorf("parse lsblk output: %w", err)
	}

	nameWidth, sizeWidth := 0, 0
	for _, d := range out.Blockdevices {
		if d.Type == "disk" {
			nameWidth = max(nameWidth, len(d.Name))
			sizeWidth = max(sizeWidth, len(d.Size))
		}
	}

	var rows []string
	for _, d := range out.Blockdevices {
		if d.Type != "disk" {
			continue
		}
		row := fmt.Sprintf("%-*s %*s", nameWidth, d.Name, sizeWidth, d.Size)
		if model := strings.TrimSpace(d.Model); model != "" {
			row += "  " + model
		}
		if slices.Contains(erased, d.Path) {
			row += "  (will be erased)"
		}
		rows = append(rows, row)
	}
	return rows, nil
}
