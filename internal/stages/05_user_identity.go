package stages

import (
	"context"
	"maps"
	"slices"
	"strings"

	"installer/internal/commands/hostidentity"
	"installer/internal/pipeline"
	"installer/internal/runner"
)

// userIdentity gives every user on the host an age key of their own on the
// target and records the recipients, for the secrets stages. A user who is no
// longer on the host drops out of the record.
func userIdentity(ctx context.Context, env *pipeline.Env) error {
	info, err := hostInfo(ctx, env)
	if err != nil {
		return err
	}
	users, err := hostUsers(ctx, env)
	if err != nil {
		return err
	}
	recipients := make(map[string]string, len(users))
	for _, user := range users {
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

// userIdentityOutdated asks for 1.3 again when the host's users are not the
// ones whose recipients are recorded, so the secrets stages after it follow
// a user added to or removed from the host.
func userIdentityOutdated(ctx context.Context, env *pipeline.Env) (string, error) {
	users, err := hostUsers(ctx, env)
	if err != nil {
		return "", err
	}
	recorded := slices.Sorted(maps.Keys(env.State.UserRecipients))
	if !slices.Equal(users, recorded) {
		return "the users of " + env.Host + " are " + list(users) + ", the recorded ones " + list(recorded), nil
	}
	for _, u := range users {
		if !strings.HasPrefix(env.State.UserRecipients[u], "age1") {
			return "no recipient is recorded for " + u, nil
		}
	}
	return "", nil
}

// list is names for a message: comma-separated, or "none".
func list(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}
