package stages

import (
	"context"
	"fmt"
	"strings"

	"installer/internal/commands/luks"
	"installer/internal/pipeline"
	"installer/internal/runner"
)

// cleanup checks that the install left nothing on the target that unlocks its
// disk or reaches GitHub: no LUKS passphrase file and no forwarded agent. A
// passphrase file still there is removed. Earlier stages need no cleanup of
// their own when they fail: the passphrase only ever lives in the ISO's RAM,
// and sshd removes a forwarded agent when its session ends.
func cleanup(ctx context.Context, env *pipeline.Env) error {
	return runner.Parallel(ctx,
		runner.Step{Title: "Check LUKS passphrase", Fn: func(ctx context.Context) error {
			removed, err := luks.Remove(ctx, env.Remote)
			if removed {
				runner.Warn(ctx, "/tmp/disko-password was still on "+env.Host+"; removed it")
			}
			return err
		}},
		runner.Step{Title: "Check forwarded agents", Fn: func(ctx context.Context) error {
			sockets, err := env.Remote.ForwardedAgents(ctx)
			if err != nil {
				return err
			}
			if len(sockets) > 0 {
				return fmt.Errorf("an ssh-agent is still forwarded to %s at %s\nclose the ssh session that forwards it (ssh -A), then run the installer again",
					env.Host, strings.Join(sockets, ", "))
			}
			return nil
		}},
	)
}
