// Package hardwareconfig gets a host's hardware config into its config flake.
package hardwareconfig

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"installer/internal/commands/git"
	"installer/internal/machine"
	"installer/internal/runner"
)

// commandNotFound is the exit code of a shell that cannot find a command.
const commandNotFound = 127

// Ensure makes sure the hardware config at path (relative to the config's root)
// exists and is known to git. When it is missing, it generates it on target, asking
// the operator first unless approved. It leaves an existing file alone. On target it
// only prints, so nothing changes there.
func Ensure(ctx context.Context, config git.Repo, target machine.Machine, path string, approved bool) error {
	// The path comes from the config, unchecked, and decides where a file is written.
	if !filepath.IsLocal(path) {
		return fmt.Errorf("hardware config path %q is not inside the config", path)
	}
	file := filepath.Join(config.Dir, path)
	if _, err := config.Machine.Quiet().Run(ctx, "test", "-e", file); err != nil {
		if !approved {
			if err := confirmGenerate(ctx, file); err != nil {
				return err
			}
		}
		if err := generate(ctx, config.Machine, target, file); err != nil {
			return err
		}
	}
	return config.Add(ctx, path)
}

// confirmGenerate requires the operator to type "yes" to generate file.
func confirmGenerate(ctx context.Context, file string) error {
	return runner.RequireTyped(ctx, fmt.Sprintf("No hardware config at %s. Type 'yes' to generate it there from the target: ", file), "yes",
		fmt.Errorf("no hardware config at %s: pass --generate-hardware to generate it, or add it yourself", file),
		fmt.Errorf("aborted: no hardware config at %s; add it yourself or generate it on the next run", file))
}

// generate writes the hardware config of target to file on local.
func generate(ctx context.Context, local, target machine.Machine, file string) error {
	// --no-filesystems: disko defines them, and a second definition conflicts.
	out, err := target.Run(ctx, "nixos-generate-config", "--no-filesystems", "--show-hardware-config")
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == commandNotFound {
		return errors.New("nixos-generate-config is missing on the target; boot it from the NixOS ISO")
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) == "" {
		return errors.New("nixos-generate-config printed nothing on the target")
	}
	_, err = local.RunStdin(ctx, strings.NewReader(out), "sh", "-c", `mkdir -p "$(dirname "$1")" && cat > "$1"`, "sh", file)
	return err
}
