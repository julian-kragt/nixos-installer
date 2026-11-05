package stages

import (
	"context"
	"fmt"
	"net"
	"os/exec"

	"installer/internal/machine"
	"installer/internal/pipeline"
	"installer/internal/runner"
)

// environmentPreparation checks the provider machine and the target before
// anything touches the target. It runs on every invocation.
func environmentPreparation(ctx context.Context, env *pipeline.Env) error {
	access := machine.Access{
		Local:     env.Local,
		Remote:    env.Remote,
		Addr:      env.Target,
		Pin:       env.Pin,
		Installed: env.State.IsCompleted(stageBaseInstall),
	}
	return runner.RunSteps(ctx,
		runner.Step{Title: "Check local tools", Fn: checkLocalTools},
		runner.Step{Title: "Check internet connection", Fn: func(ctx context.Context) error {
			return machine.Dial(ctx, "8.8.8.8:53")
		}},
		runner.Step{Title: "Check target is reachable", Fn: func(ctx context.Context) error {
			return machine.Dial(ctx, net.JoinHostPort(env.Target, "22"))
		}},
		runner.Step{Title: "Check SSH host key", Fn: access.CheckHostKey},
		runner.Step{Title: "Set up SSH access", Fn: func(ctx context.Context) error {
			return access.SetupKeyLogin(ctx, env.SSHPassword)
		}},
	)
}

func checkLocalTools(context.Context) error {
	for _, tool := range []string{"ssh", "git", "sops", "mkpasswd", "vim"} {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("%s not found in PATH", tool)
		}
	}
	return nil
}
