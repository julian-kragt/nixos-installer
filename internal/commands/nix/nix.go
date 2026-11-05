// Package nix evaluates the outputs nixos-bootstrap adds to the config flake.
package nix

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"installer/internal/machine"
)

// Config is the config flake at Dir, an absolute path, so nix finds the
// repository around it. It is evaluated on Machine.
type Config struct {
	Machine machine.Machine
	Dir     string
}

// evalJSON runs `nix eval --json installable extra...` on m and decodes the
// result into out. Only stdout is decoded, so a `warning:` on stderr does no harm.
func evalJSON(ctx context.Context, m machine.Machine, installable string, out any, extra ...string) error {
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

// eval evaluates attr of the flake. It never writes the flake's lock file.
func (c Config) eval(ctx context.Context, attr string, out any, extra ...string) error {
	args := append([]string{"--no-write-lock-file"}, extra...)
	return evalJSON(ctx, c.Machine, c.Dir+"#"+attr, out, args...)
}

// nixCommandDisabled is what nix says when `nix config show` needs the
// nix-command feature it does not have.
const nixCommandDisabled = "experimental Nix feature 'nix-command' is disabled"

// Features returns the experimental features nix has enabled on m. Without
// nix-command, which `nix config show` itself needs, that is none: nix's
// message is only seen when m is not quiet.
func Features(ctx context.Context, m machine.Machine) ([]string, error) {
	out, err := m.Run(ctx, "nix", "config", "show", "experimental-features")
	if err != nil && strings.Contains(err.Error(), nixCommandDisabled) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

// HostInfo is nixos-bootstrap's installerData.<host>: the config's entry for
// one host, checked.
type HostInfo struct {
	System             string          `json:"system"`
	Users              map[string]User `json:"users"`
	RootAuthorizedKeys []string        `json:"rootAuthorizedKeys"`
	HardwareConfigPath string          `json:"hardwareConfigPath"`
	// Disks holds the devices an install wipes, by disk name.
	Disks map[string]string `json:"disks"`
}

// User is one normal user of a host. UID is nil when NixOS allocates it.
type User struct {
	UID  *int   `json:"uid"`
	Home string `json:"home"`
}

// HostInfo evaluates `installerData.<host>`.
func (c Config) HostInfo(ctx context.Context, host string) (HostInfo, error) {
	var info HostInfo
	err := c.eval(ctx, "installerData."+host, &info)
	return info, err
}

// HardwareConfigPath reads the path from the config's own entry, relative to the
// config's root. installerData checks that the file exists, so it cannot tell where
// a missing file belongs. Unchecked: the caller must not trust the value.
func (c Config) HardwareConfigPath(ctx context.Context, host string) (string, error) {
	var path string
	err := c.eval(ctx, "bootstrapEntries."+host+".hardwareConfigPath", &path)
	return path, err
}

// Hosts returns the hosts the config has an entry for.
func (c Config) Hosts(ctx context.Context) ([]string, error) {
	var hosts []string
	err := c.eval(ctx, "installerData", &hosts, "--apply", "builtins.attrNames")
	return hosts, err
}

// CheckBootstrapSystem evaluates the host's whole bootstrap system. installerData
// only checks the entry's fields; this also runs disko's options, NixOS's
// assertions and the hardware config.
func (c Config) CheckBootstrapSystem(ctx context.Context, host string) error {
	var drvPath string
	return c.eval(ctx, "nixosConfigurations."+host+"-bootstrap.config.system.build.toplevel.drvPath", &drvPath)
}
