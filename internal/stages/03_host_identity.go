package stages

import (
	"context"

	"installer/internal/commands/hostidentity"
	"installer/internal/pipeline"
	"installer/internal/runner"
)

// hostIdentity gives the target its own age key and records the recipient, for
// the secrets stages.
func hostIdentity(ctx context.Context, env *pipeline.Env) error {
	return runner.Run(ctx, "Ensure host age key", func(ctx context.Context) error {
		recipient, err := hostidentity.Recipient(ctx, env.Remote, hostidentity.KeyFile)
		if err != nil {
			return err
		}
		env.State.HostRecipient = recipient
		runner.Info(ctx, "Host recipient: "+recipient)
		return nil
	})
}
