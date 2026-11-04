// Package nix evaluates nixos-bootstrap's outputs for the config flake.
package nix

import (
	"context"
	"encoding/json"
	"fmt"

	"installer/internal/pipeline"
)

// EvalJSON runs `nix eval --json installable extra...` on m and decodes the
// result into out. Only stdout is decoded, so a `warning:` on stderr does no harm.
func EvalJSON(ctx context.Context, m pipeline.Machine, installable string, out any, extra ...string) error {
	args := append([]string{"eval", "--json", installable}, extra...)
	stdout, err := m.Run(ctx, "nix", args...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(stdout), out); err != nil {
		return fmt.Errorf("nix eval %s: decode: %w", installable, err)
	}
	return nil
}

// Flakes names the nixos-bootstrap flake and the config flake it is evaluated
// for. Config is an absolute path, so nix finds the repository around it.
type Flakes struct {
	Bootstrap string
	Config    string
}

// eval evaluates attr of the bootstrap flake with its `config` input set to
// f.Config. It never writes the bootstrap flake's lock file.
func (f Flakes) eval(ctx context.Context, m pipeline.Machine, attr string, out any, extra ...string) error {
	args := append([]string{"--override-input", "config", f.Config, "--no-write-lock-file"}, extra...)
	return EvalJSON(ctx, m, f.Bootstrap+"#"+attr, out, args...)
}

// HostInfo is nixos-bootstrap's installerData.<host>: the config's entry for
// one host, checked.
type HostInfo struct {
	System             string          `json:"system"`
	Users              map[string]User `json:"users"`
	RootAuthorizedKeys []string        `json:"rootAuthorizedKeys"`
	HardwareConfigPath string          `json:"hardwareConfigPath"`
}

// User is one normal user of a host. UID is nil when NixOS allocates it.
type User struct {
	UID  *int   `json:"uid"`
	Home string `json:"home"`
}

// EvalHostInfo evaluates `installerData.<host>`.
func EvalHostInfo(ctx context.Context, m pipeline.Machine, f Flakes, host string) (HostInfo, error) {
	var info HostInfo
	err := f.eval(ctx, m, "installerData."+host, &info)
	return info, err
}

// ListHosts returns the hosts the config has an entry for.
func ListHosts(ctx context.Context, m pipeline.Machine, f Flakes) ([]string, error) {
	var hosts []string
	err := f.eval(ctx, m, "installerData", &hosts, "--apply", "builtins.attrNames")
	return hosts, err
}

// EvalDisks returns the devices of the host's disko layout, by disk name.
// It reads the bootstrap system; installerData.<host>.disks holds the same map.
func EvalDisks(ctx context.Context, m pipeline.Machine, f Flakes, host string) (map[string]string, error) {
	var disks map[string]string
	err := f.eval(ctx, m, "nixosConfigurations."+host+".config.disko.devices.disk", &disks,
		"--apply", "builtins.mapAttrs (_: d: d.device)")
	return disks, err
}

// CheckBootstrapSystem evaluates the host's whole bootstrap system. installerData
// only checks the entry's fields; this also runs disko's options, NixOS's
// assertions and the hardware config.
func CheckBootstrapSystem(ctx context.Context, m pipeline.Machine, f Flakes, host string) error {
	var drvPath string
	return f.eval(ctx, m, "nixosConfigurations."+host+".config.system.build.toplevel.drvPath", &drvPath)
}
