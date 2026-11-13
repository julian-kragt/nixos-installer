package stages

import (
	"context"

	"github.com/julian-kragt/nixos-installer/internal/pipeline"
	"github.com/julian-kragt/nixos-installer/internal/runner"
)

// secretsRepository reads where nix-secrets keeps each kind of file, from its
// installerSecretsData output, and records it for the rest of the install. It
// checks that the clone can take the host's secrets: nothing uncommitted
// besides the files the secrets phase writes for the host, and level with its
// upstream, so the push in 2.4 publishes only that.
func secretsRepository(ctx context.Context, env *pipeline.Env) error {
	layout, err := secretsFlake(env).SecretsLayout(ctx)
	if err != nil {
		return err
	}
	env.State.SecretsLayout = &layout
	runner.Info(ctx, "nix-secrets keeps user secrets "+string(layout.UserMode))
	managed := func(path string) bool { return layout.Managed(env.Host, path) }
	if err := secretsRepo(env).RequireCommitted(ctx, managed); err != nil {
		return err
	}
	return secretsRepo(env).RequireInSync(ctx)
}
