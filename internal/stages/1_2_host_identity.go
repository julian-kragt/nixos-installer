package stages

import (
	"context"
	"strings"

	"github.com/julian-kragt/nixos-installer/internal/commands/hostidentity"
	"github.com/julian-kragt/nixos-installer/internal/pipeline"
	"github.com/julian-kragt/nixos-installer/internal/runner"
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

// hostIdentityOutdated asks for 1.2 again when no host recipient is recorded.
func hostIdentityOutdated(_ context.Context, env *pipeline.Env) (string, error) {
	if !strings.HasPrefix(env.State.HostRecipient, "age1") {
		return "no host recipient is recorded", nil
	}
	return "", nil
}
