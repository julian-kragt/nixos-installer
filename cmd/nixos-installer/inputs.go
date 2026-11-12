package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/julian-kragt/nixos-installer/internal/commands/nix"
	"github.com/julian-kragt/nixos-installer/internal/machine"
	"github.com/julian-kragt/nixos-installer/internal/pipeline"
	"github.com/julian-kragt/nixos-installer/internal/runner"
	"github.com/julian-kragt/nixos-installer/internal/stages"
)

// resolveInputs asks for whatever the flags left empty (config, secrets, host,
// target) and loads the persisted state of the chosen host, since state is
// keyed by host name. The target is asked last: how to prepare it is shown
// until the base install is done, which is known once the state is loaded,
// and on a reinstall. With in.Reinstall the state starts fresh, but what is
// saved for the host is only replaced once 0.1 has the wipe confirmed
// (pipeline.Env.ConfirmReinstall). in is completed in place.
func resolveInputs(ctx context.Context, in *pipeline.Inputs, stateDir string) (*pipeline.State, error) {
	var st *pipeline.State
	err := runner.Run(ctx, "Checking inputs", func(ctx context.Context) error {
		configDir, err := askConfigDir(ctx, in.ConfigDir)
		if err != nil {
			return err
		}
		secretsDir, err := askSecretsDir(ctx, in.SecretsDir)
		if err != nil {
			return err
		}
		hosts, err := listHosts(ctx, configDir)
		if err != nil {
			return err
		}
		host, err := pickHost(ctx, hosts, in.Host)
		if err != nil {
			return err
		}
		in.ConfigDir, in.SecretsDir, in.Host = configDir, secretsDir, host

		if in.Reinstall {
			st, err = pipeline.StartReinstall(stateDir, host)
		} else {
			st, err = pipeline.LoadState(stateDir, host)
		}
		if err != nil {
			return err
		}
		if in.Reinstall || !stages.BaseInstalled(st) {
			if err := showInstructions(ctx); err != nil {
				return err
			}
		}
		target, err := askTarget(ctx, in.Target)
		if err != nil {
			return err
		}
		in.Target = target
		if in.Reinstall {
			runner.Info(ctx, "Reinstall: the saved state and pinned host key of "+host+" are replaced once the wipe is confirmed in 0.1")
		}
		runner.Info(ctx, "Target: "+target)
		runner.Info(ctx, "Host: "+host)
		return nil
	})
	return st, err
}

// askTarget returns target, or asks for the target IP until the answer is
// valid when target is empty.
func askTarget(ctx context.Context, target string) (string, error) {
	if target != "" {
		return target, nil
	}
	return askUntil(ctx, "Target IP address", "--target", func(s string) (string, error) {
		if validateTarget(s) != nil {
			return "", fmt.Errorf("%q is not a valid IP", s)
		}
		return s, nil
	})
}

// askConfigDir returns dir, or asks for the config flake's directory until it
// holds a flake.nix when dir is empty.
func askConfigDir(ctx context.Context, dir string) (string, error) {
	if dir != "" {
		return checkConfigDir(dir)
	}
	return askUntil(ctx, "Path to the config flake", "--config", checkConfigDir)
}

// checkConfigDir returns the absolute path of dir if it holds a flake.nix.
func checkConfigDir(dir string) (string, error) { return dirWith(dir, "flake.nix") }

// askSecretsDir returns dir, or asks for the nix-secrets repository until it
// holds a .sops.yaml when dir is empty.
func askSecretsDir(ctx context.Context, dir string) (string, error) {
	if dir != "" {
		return checkSecretsDir(dir)
	}
	return askUntil(ctx, "Path to the secrets repository (the config's nix-secrets input)", "--secrets", checkSecretsDir)
}

// checkSecretsDir returns the absolute path of dir if it holds a .sops.yaml.
func checkSecretsDir(dir string) (string, error) { return dirWith(dir, ".sops.yaml") }

// dirWith returns the absolute path of dir, with a leading ~ expanded, if it
// holds file.
func dirWith(dir, file string) (string, error) {
	dir = expandHome(dir)
	if _, err := os.Stat(filepath.Join(dir, file)); dir == "" || err != nil {
		return "", fmt.Errorf("%q has no %s", dir, file)
	}
	return filepath.Abs(dir)
}

// askUntil asks for label until check accepts the answer and returns what
// check made of it. A rejected answer is explained in the next prompt. Without
// an operator to ask, the error names the flag to use instead.
func askUntil(ctx context.Context, label, flag string, check func(string) (string, error)) (string, error) {
	prompt := label + ": "
	for {
		line, err := runner.Ask(ctx, prompt)
		if errors.Is(err, io.EOF) {
			return "", errors.New(flag + " is required")
		}
		if err != nil {
			return "", err
		}
		v, err := check(line)
		if err == nil {
			return v, nil
		}
		prompt = fmt.Sprintf("%s (%v): ", label, err)
	}
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

// listHosts returns the hosts that are valid install targets: the config's
// entries, as nixos-bootstrap reports them.
func listHosts(ctx context.Context, configDir string) ([]string, error) {
	if _, err := exec.LookPath("nix"); err != nil {
		return nil, errors.New("nix not found in PATH")
	}
	hosts, err := nix.Config{Machine: machine.Local(), Dir: configDir}.Hosts(ctx)
	if err != nil {
		return nil, fmt.Errorf("list hosts: %w", err)
	}
	if len(hosts) == 0 {
		return nil, fmt.Errorf("no installable hosts: the `bootstrapEntries` output of %s is empty", configDir)
	}
	return hosts, nil
}

// pickHost resolves the host to install. A non-empty want must be in hosts;
// otherwise the operator chooses from a numbered list.
func pickHost(ctx context.Context, hosts []string, want string) (string, error) {
	if want != "" {
		if slices.Contains(hosts, want) {
			return want, nil
		}
		return "", fmt.Errorf("unknown host %q (available: %s)", want, strings.Join(hosts, ", "))
	}
	if len(hosts) == 1 {
		return hosts[0], nil
	}
	i, err := runner.Choose(ctx, "Select a host to install:", hosts)
	if err != nil {
		return "", err
	}
	return hosts[i], nil
}
