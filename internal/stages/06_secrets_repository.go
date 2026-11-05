package stages

import (
	"context"

	"installer/internal/pipeline"
	"installer/internal/secrets"
)

// secretsRepository checks that the nix-secrets clone can take the host's
// secrets: nothing uncommitted besides the files the secrets phase writes for
// the host, and level with its upstream, so the push in 2.4 publishes only that.
func secretsRepository(ctx context.Context, env *pipeline.Env) error {
	users, err := hostUsers(ctx, env)
	if err != nil {
		return err
	}
	managed, err := secrets.Managed(env.SecretsDir, env.Host, users)
	if err != nil {
		return err
	}
	if err := secretsRepo(env).RequireCommitted(ctx, managed...); err != nil {
		return err
	}
	return secretsRepo(env).RequireInSync(ctx)
}
