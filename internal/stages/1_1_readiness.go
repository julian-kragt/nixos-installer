package stages

import (
	"context"
	"fmt"
	"slices"

	"github.com/julian-kragt/nixos-installer/internal/commands/nix"
	"github.com/julian-kragt/nixos-installer/internal/machine"
	"github.com/julian-kragt/nixos-installer/internal/pipeline"
	"github.com/julian-kragt/nixos-installer/internal/runner"
)

// bootstrapHint tells the operator where the bootstrap system gets what
// readiness checks: the nixos-bootstrap version the config locks.
const bootstrapHint = "the bootstrap system comes from the config's nixos-bootstrap input (https://github.com/julian-kragt/nixos-bootstrap); update that input"

// readiness checks that the installed bootstrap system has what the later
// stages run on it.
func readiness(ctx context.Context, env *pipeline.Env) error {
	return runner.Parallel(ctx,
		runner.Step{Title: "Check tools", Fn: func(ctx context.Context) error {
			// sops, mkpasswd and vim are run by the console helper in 2.3.
			return checkTools(ctx, env.Remote, "age-keygen", "runuser", "git", "sops", "mkpasswd", "vim", "systemd-run", "nix", "curl")
		}},
		runner.Step{Title: "Check nix features", Fn: func(ctx context.Context) error {
			return checkNixFeatures(ctx, env.Remote, "nix-command", "flakes", "pipe-operators")
		}},
		runner.Step{Title: "Check GitHub", Fn: func(ctx context.Context) error {
			return checkGitHub(ctx, env.Remote)
		}},
	)
}

// checkTools fails with the first of tools that target does not have.
func checkTools(ctx context.Context, target machine.Machine, tools ...string) error {
	for _, tool := range tools {
		if !target.HasCommand(ctx, tool) {
			return fmt.Errorf("%s is missing on the target; %s", tool, bootstrapHint)
		}
	}
	return nil
}

// checkNixFeatures fails unless nix on target has every experimental feature in want.
func checkNixFeatures(ctx context.Context, target machine.Machine, want ...string) error {
	have, err := nix.Features(ctx, target)
	if err != nil {
		return err
	}
	for _, f := range want {
		if !slices.Contains(have, f) {
			return fmt.Errorf("nix on the target does not enable the %s feature; %s", f, bootstrapHint)
		}
	}
	return nil
}

// checkGitHub fails unless target reaches GitHub and knows its host key, so
// git over ssh does not stop at a host key question.
func checkGitHub(ctx context.Context, target machine.Machine) error {
	if _, err := target.Run(ctx, "curl", "-fsS", "--max-time", "10", "-o", "/dev/null", "https://github.com"); err != nil {
		return fmt.Errorf("target cannot reach github.com: %w", err)
	}
	if _, err := target.Quiet().Run(ctx, "ssh-keygen", "-F", "github.com", "-f", "/etc/ssh/ssh_known_hosts"); err != nil {
		return fmt.Errorf("github.com is not a known host on the target; %s", bootstrapHint)
	}
	return nil
}
