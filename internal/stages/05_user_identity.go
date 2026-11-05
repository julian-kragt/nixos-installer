package stages

import (
	"context"
	"maps"
	"slices"

	"installer/internal/commands/hostidentity"
	"installer/internal/pipeline"
	"installer/internal/runner"
)

// userIdentity gives every user on the host an age key of their own on the
// target and records the recipients, for the secrets stages. A user who is no
// longer on the host drops out of the record.
func userIdentity(ctx context.Context, env *pipeline.Env) error {
	info, err := configFlake(env).HostInfo(ctx, env.Host)
	if err != nil {
		return err
	}
	recipients := make(map[string]string, len(info.Users))
	for _, user := range slices.Sorted(maps.Keys(info.Users)) {
		err := runner.Run(ctx, "Ensure age key of "+user, func(ctx context.Context) error {
			recipient, err := hostidentity.UserRecipient(ctx, env.Remote, user, info.Users[user].Home)
			if err != nil {
				return err
			}
			recipients[user] = recipient
			runner.Info(ctx, "Recipient: "+recipient)
			return nil
		})
		if err != nil {
			return err
		}
	}
	env.State.UserRecipients = recipients
	return nil
}
