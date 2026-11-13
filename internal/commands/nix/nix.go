// Package nix evaluates the outputs nixos-bootstrap adds to the config flake.
package nix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/julian-kragt/nixos-installer/internal/machine"
	"github.com/julian-kragt/nixos-installer/internal/secrets"
)

// Config is the config flake at Dir, an absolute path, so nix finds the
// repository around it. It is evaluated on Machine.
type Config struct {
	Machine machine.Machine
	Dir     string
	// Rev, when set, is the commit of Dir's repository that evaluations read
	// instead of the working tree. UpdateInput and LockedRev always work on Dir.
	Rev string
}

// At returns c evaluated at the commit rev.
func (c Config) At(rev string) Config {
	c.Rev = rev
	return c
}

// ref is the flake reference evaluations use: Dir, or Dir's repository at Rev.
func (c Config) ref() string {
	if c.Rev == "" {
		return c.Dir
	}
	return "git+file://" + c.Dir + "?rev=" + c.Rev
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
	return evalJSON(ctx, c.Machine, c.ref()+"#"+attr, out, args...)
}

// SecretsLayout reads the layout of the nix-secrets repository at c: its
// installerSecretsData output, secrets-entry.nix checked by nixos-bootstrap. It
// is secrets.ErrNoEntry when the working tree has no secrets-entry.nix.
func (c Config) SecretsLayout(ctx context.Context) (secrets.Layout, error) {
	if _, err := os.Stat(filepath.Join(c.Dir, secrets.EntryFile)); c.Rev == "" && errors.Is(err, os.ErrNotExist) {
		return secrets.Layout{}, secrets.ErrNoEntry
	}
	var l secrets.Layout
	if err := c.eval(ctx, "installerSecretsData", &l); err != nil {
		return secrets.Layout{}, fmt.Errorf("read the nix-secrets layout: %w", err)
	}
	if err := l.Check(); err != nil {
		return secrets.Layout{}, err
	}
	return l, nil
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

// PasswordUsers returns, sorted, the users whose password the host's full
// system takes from sops: those with a secret passwords/<user>.
func (c Config) PasswordUsers(ctx context.Context, host string) ([]string, error) {
	var names []string
	err := c.eval(ctx, "nixosConfigurations."+host+".config.sops.secrets", &names, "--apply", "builtins.attrNames")
	return passwordUsers(names), err
}

// passwordUsers returns the users of the passwords/<user> names among a
// system's sops secret names, sorted.
func passwordUsers(names []string) []string {
	var users []string
	for _, name := range names {
		if user, ok := strings.CutPrefix(name, "passwords/"); ok && user != "" && !strings.Contains(user, "/") {
			users = append(users, user)
		}
	}
	slices.Sort(users)
	return users
}

// Toplevel returns the store path of the host's full system, which the target
// runs once it has switched to it.
func (c Config) Toplevel(ctx context.Context, host string) (string, error) {
	var path string
	err := c.eval(ctx, "nixosConfigurations."+host+".config.system.build.toplevel.outPath", &path)
	return path, err
}

// InputURL returns the URL the flake's lock file gives as the original of the
// git input name, such as ssh://git@github.com/owner/repo.git.
func (c Config) InputURL(ctx context.Context, name string) (string, error) {
	var metadata struct {
		Locks json.RawMessage `json:"locks"`
	}
	if err := evalMetadata(ctx, c, &metadata); err != nil {
		return "", err
	}
	return inputURL(metadata.Locks, name)
}

// evalMetadata decodes `nix flake metadata --json` of the flake into out. It
// never writes the flake's lock file.
func evalMetadata(ctx context.Context, c Config, out any) error {
	stdout, err := c.Machine.Run(ctx, "nix", "flake", "metadata", "--json", "--no-write-lock-file", c.ref())
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(stdout), out); err != nil {
		return fmt.Errorf("nix flake metadata %s: decode: %w", c.ref(), err)
	}
	return nil
}

// UpdateInput locks the flake input name at what its URL points to now, in
// the flake's lock file.
func (c Config) UpdateInput(ctx context.Context, name string) error {
	_, err := c.Machine.Run(ctx, "nix", "flake", "update", name, "--flake", c.Dir)
	return err
}

// LockedRev returns the commit the flake's lock file locks the input name at.
// Dir is on the provider, so the file is read here.
func (c Config) LockedRev(name string) (string, error) {
	lock, err := os.ReadFile(filepath.Join(c.Dir, "flake.lock"))
	if err != nil {
		return "", err
	}
	return lockedRev(lock, name)
}

// lockedRev returns the rev that the lock file lock locks the root's input at.
func lockedRev(lock []byte, input string) (string, error) {
	node, err := lockNode(lock, input)
	if err != nil {
		return "", err
	}
	if node.Locked.Rev == "" {
		return "", fmt.Errorf("flake.lock locks input %s at no git rev", input)
	}
	return node.Locked.Rev, nil
}

// inputURL returns the original URL of the root's git input in the lock file lock.
func inputURL(lock []byte, input string) (string, error) {
	node, err := lockNode(lock, input)
	if err != nil {
		return "", err
	}
	if node.Original.Type != "git" || node.Original.URL == "" {
		return "", fmt.Errorf("flake.lock: input %s is not a git URL", input)
	}
	return node.Original.URL, nil
}

// lockedNode is a node of a lock file, with what the installer reads of it.
type lockedNode struct {
	Inputs map[string]json.RawMessage `json:"inputs"`
	Locked struct {
		Rev string `json:"rev"`
	} `json:"locked"`
	Original struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"original"`
}

// lockNode returns the node of the root's input in the lock file lock.
func lockNode(lock []byte, input string) (lockedNode, error) {
	var file struct {
		Root  string                `json:"root"`
		Nodes map[string]lockedNode `json:"nodes"`
	}
	if err := json.Unmarshal(lock, &file); err != nil {
		return lockedNode{}, fmt.Errorf("flake.lock: %w", err)
	}
	ref, ok := file.Nodes[file.Root].Inputs[input]
	if !ok {
		return lockedNode{}, fmt.Errorf("flake.lock has no input %s", input)
	}
	// A string names the input's node; a list follows another flake's input.
	var node string
	if err := json.Unmarshal(ref, &node); err != nil {
		return lockedNode{}, fmt.Errorf("flake.lock: input %s follows another input", input)
	}
	return file.Nodes[node], nil
}
