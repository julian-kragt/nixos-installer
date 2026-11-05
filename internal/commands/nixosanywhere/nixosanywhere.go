// Package nixosanywhere installs a host's bootstrap system with nixos-anywhere
// and tells what a target is running.
package nixosanywhere

import (
	"context"
	"fmt"
	"strings"

	"installer/internal/commands/git"
	"installer/internal/machine"
)

// ref is the nixos-anywhere the install runs.
const ref = "github:nix-community/nixos-anywhere/1.13.0"

// osReleasePath is a variable so tests can change it.
var osReleasePath = "/etc/os-release"

// TargetKind is what a target is running.
type TargetKind int

const (
	TargetOther     TargetKind = iota
	TargetISO                  // the NixOS installer: nixos-anywhere does not kexec it
	TargetInstalled            // NixOS under the host's name
)

// Inspect reads what target runs. It goes over the pinned connection, so it is
// also what checks the pin right before nixos-anywhere, which cannot be given
// one: its own StrictHostKeyChecking=no comes first and ssh takes the first value
// of an option.
func Inspect(ctx context.Context, target machine.Machine, host string) (TargetKind, error) {
	hostname, err := target.Run(ctx, "hostname")
	if err != nil {
		return TargetOther, err
	}
	osRelease, err := target.Run(ctx, "cat", osReleasePath)
	if err != nil {
		return TargetOther, err
	}
	return classifyTarget(strings.TrimSpace(hostname), osRelease, host), nil
}

// classifyTarget decides from the hostname and /etc/os-release. The installer
// test is the one nixos-anywhere uses to skip its kexec. Testing for
// /run/current-system would not do: the ISO has that too.
func classifyTarget(hostname, osRelease, host string) TargetKind {
	fields := map[string]string{}
	for _, line := range strings.Split(osRelease, "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			fields[key] = strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}
	switch {
	case fields["ID"] != "nixos":
		return TargetOther
	case fields["VARIANT_ID"] == "installer":
		return TargetISO
	case hostname == host:
		return TargetInstalled
	}
	return TargetOther
}

// Install installs the host's bootstrap system from the config flake on target.
// It runs in the config's root, where a relative --generate-hardware-config path
// lands. All its output goes to stderr, which streams to the runner.
func Install(ctx context.Context, config git.Repo, host, target, hardwareConfigPath string) error {
	prefix, err := config.Prefix(ctx)
	if err != nil {
		return err
	}
	args := append([]string{"-c", `cd "$1" && shift && exec "$@" 1>&2`, "sh", config.Dir, "nix"},
		installArgs(configFlakeURL(config.Dir, prefix), host, target, hardwareConfigPath)...)
	if _, err := config.Machine.Run(ctx, "sh", args...); err != nil {
		return fmt.Errorf("nixos-anywhere: %w", err)
	}
	return nil
}

// installArgs are the arguments of `nix` for the install. hardwareConfigPath is
// relative to the config's root.
func installArgs(config, host, target, hardwareConfigPath string) []string {
	return []string{"run", ref, "--",
		"--flake", config + "#" + host + "-bootstrap",
		"--target-host", "root@" + target,
		"--build-on", "remote",
		"--generate-hardware-config", "nixos-generate-config", hardwareConfigPath,
	}
}

// configFlakeURL is the git+file reference of the config flake at dir, prefix
// being its path below the top of the repository (as from Repo.Prefix).
func configFlakeURL(dir, prefix string) string {
	if prefix == "" {
		return "git+file://" + dir
	}
	sub := strings.TrimSuffix(prefix, "/")
	return "git+file://" + strings.TrimSuffix(dir, "/"+sub) + "?dir=" + sub
}
