// Package nix evaluates the outputs nixos-bootstrap adds to the config flake.
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

// eval evaluates attr of the config flake. config is an absolute path, so nix
// finds the repository around it. It never writes the config's lock file.
func eval(ctx context.Context, m pipeline.Machine, config, attr string, out any, extra ...string) error {
	args := append([]string{"--no-write-lock-file"}, extra...)
	return EvalJSON(ctx, m, config+"#"+attr, out, args...)
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
func EvalHostInfo(ctx context.Context, m pipeline.Machine, config, host string) (HostInfo, error) {
	var info HostInfo
	err := eval(ctx, m, config, "installerData."+host, &info)
	return info, err
}

// ListHosts returns the hosts the config has an entry for.
func ListHosts(ctx context.Context, m pipeline.Machine, config string) ([]string, error) {
	var hosts []string
	err := eval(ctx, m, config, "installerData", &hosts, "--apply", "builtins.attrNames")
	return hosts, err
}

// EvalDisks returns the devices of the host's disko layout, by disk name.
// It reads the bootstrap system; installerData.<host>.disks holds the same map.
func EvalDisks(ctx context.Context, m pipeline.Machine, config, host string) (map[string]string, error) {
	var disks map[string]string
	err := eval(ctx, m, config, "nixosConfigurations."+host+"-bootstrap.config.disko.devices.disk", &disks,
		"--apply", "builtins.mapAttrs (_: d: d.device)")
	return disks, err
}

// CheckBootstrapSystem evaluates the host's whole bootstrap system. installerData
// only checks the entry's fields; this also runs disko's options, NixOS's
// assertions and the hardware config.
func CheckBootstrapSystem(ctx context.Context, m pipeline.Machine, config, host string) error {
	var drvPath string
	return eval(ctx, m, config, "nixosConfigurations."+host+"-bootstrap.config.system.build.toplevel.drvPath", &drvPath)
}
