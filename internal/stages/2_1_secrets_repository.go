package stages

import (
	"context"

	"github.com/julian-kragt/nixos-installer/internal/pipeline"
	"github.com/julian-kragt/nixos-installer/internal/secrets"
)

// secretsRepository checks that the nix-secrets clone can take the host's
// secrets: nothing uncommitted besides the files the secrets phase writes for
// the host, and level with its upstream, so the push in 2.4 publishes only that.
func secretsRepository(ctx context.Context, env *pipeline.Env) error {
	managed := func(path string) bool { return secrets.Managed(env.Host, path) }
	if err := secretsRepo(env).RequireCommitted(ctx, managed); err != nil {
		return err
	}
	return secretsRepo(env).RequireInSync(ctx)
}
