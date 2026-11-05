// Package stages holds the stages of the install pipeline, one file per stage.
// A stage file orchestrates what happens in that stage: the logic lives in the
// packages it calls.
package stages

import (
	"context"
	"maps"
	"slices"

	"installer/internal/commands/git"
	"installer/internal/commands/nix"
	"installer/internal/pipeline"
)

// stageBaseInstall is the ID of the stage after which the target's system must
// not change any more.
const stageBaseInstall = "0.2"

// All is the pipeline: the stages in the order they run.
var All = []pipeline.Stage{
	{ID: "0.0", Name: "Environment preparation", Always: true, Run: environmentPreparation},
	{ID: "0.1", Name: "Provider preparation", Run: providerPreparation},
	{ID: stageBaseInstall, Name: "Base install", Run: baseInstall},
	{ID: "1.1", Name: "Host identity", Run: hostIdentity, Outdated: hostIdentityOutdated},
	{ID: "1.2", Name: "Readiness", Run: readiness},
	{ID: "1.3", Name: "User identity", Run: userIdentity, Outdated: userIdentityOutdated},
	{ID: "2.1", Name: "Secrets repository", Run: secretsRepository},
	{ID: "2.2", Name: "Recipients", Run: recipients},
	{ID: "2.3", Name: "Template authoring", Run: templateAuthoring},
	{ID: "2.4", Name: "Commit and push", Run: commitPush},
}

// configFlake is the config flake, evaluated on the provider.
func configFlake(env *pipeline.Env) nix.Config {
	return nix.Config{Machine: env.Local, Dir: env.ConfigDir}
}

// configRepo is the git repository of the config flake, on the provider.
func configRepo(env *pipeline.Env) git.Repo {
	return git.Repo{Machine: env.Local, Dir: env.ConfigDir}
}

// secretsRepo is the nix-secrets clone the secrets phase works in, on the provider.
func secretsRepo(env *pipeline.Env) git.Repo {
	return git.Repo{Machine: env.Local, Dir: env.SecretsDir}
}

// hostUsers returns the normal users of the host, sorted, as the config defines
// them. The list is read on every run, never stored.
func hostUsers(ctx context.Context, env *pipeline.Env) ([]string, error) {
	info, err := configFlake(env).HostInfo(ctx, env.Host)
	if err != nil {
		return nil, err
	}
	return slices.Sorted(maps.Keys(info.Users)), nil
}
