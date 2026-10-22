package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"installer/internal/pipeline"
	"installer/internal/runner"
)

// resolveInputs asks for whatever the flags left empty (target, config, host) and
// loads the persisted state of the chosen host, since state is keyed by host
// name. in is completed in place.
func resolveInputs(ctx context.Context, in *pipeline.Inputs, stateDir string) (*pipeline.State, error) {
	if err := showInstructions(ctx); err != nil {
		return nil, err
	}
	var st *pipeline.State
	err := runner.Run(ctx, "Checking inputs", func(ctx context.Context) error {
		target, err := askTarget(ctx, in.Target)
		if err != nil {
			return err
		}
		configDir, err := askConfigDir(ctx, in.ConfigDir)
		if err != nil {
			return err
		}
		hosts, err := listHosts(configDir)
		if err != nil {
			return err
		}
		host, err := pickHost(ctx, hosts, in.Host)
		if err != nil {
			return err
		}
		in.Target, in.ConfigDir, in.Host = target, configDir, host

		st, err = pipeline.LoadState(stateDir, host)
		if err != nil {
			return err
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

// askConfigDir returns dir, or asks for the nixos-config directory until it
// holds a hosts/nixos directory when dir is empty. A leading ~ is expanded.
func askConfigDir(ctx context.Context, dir string) (string, error) {
	if dir != "" {
		return filepath.Abs(expandHome(dir))
	}
	return askUntil(ctx, "Path to nixos-config", "--config", func(s string) (string, error) {
		s = expandHome(s)
		if fi, err := os.Stat(filepath.Join(s, "hosts", "nixos")); s == "" || err != nil || !fi.IsDir() {
			return "", fmt.Errorf("%q has no hosts/nixos", s)
		}
		return filepath.Abs(s)
	})
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

// listHosts returns the hosts that are valid install targets: directories
// under <configDir>/hosts/nixos that contain bootstrap.nix.
func listHosts(configDir string) ([]string, error) {
	root := filepath.Join(configDir, "hosts", "nixos")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("list hosts: %w", err)
	}
	var hosts []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, e.Name(), "bootstrap.nix")); err == nil {
			hosts = append(hosts, e.Name())
		}
	}
	slices.Sort(hosts)
	if len(hosts) == 0 {
		return nil, fmt.Errorf("no installable hosts found in %s", root)
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
