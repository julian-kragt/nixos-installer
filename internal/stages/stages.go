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
	"installer/internal/secrets"
)

// stageBaseInstall is the ID of the stage after which the target's system must
// not change any more.
const stageBaseInstall = "0.2"

// All is the pipeline: the stages in the order they run.
var All = []pipeline.Stage{
	{ID: "0.0", Name: "Environment preparation", Always: true, Run: environmentPreparation},
	{ID: "0.1", Name: "Provider preparation", Run: providerPreparation},
	{ID: stageBaseInstall, Name: "Base install", Run: baseInstall},
	{ID: "1.1", Name: "Readiness", Run: readiness},
	{ID: "1.2", Name: "Host identity", Run: hostIdentity, Outdated: hostIdentityOutdated},
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

// hostInfos holds the config's entry for the host of each run, for hostInfo.
// Stages run one at a time, so it needs no lock.
var hostInfos = map[*pipeline.Env]nix.HostInfo{}

// hostInfo returns the config's entry for the host, evaluated once per run
// and never saved. 0.1 and 0.2 evaluate it themselves: 0.1 may change the
// config.
func hostInfo(ctx context.Context, env *pipeline.Env) (nix.HostInfo, error) {
	if info, ok := hostInfos[env]; ok {
		return info, nil
	}
	info, err := configFlake(env).HostInfo(ctx, env.Host)
	if err != nil {
		return nix.HostInfo{}, err
	}
	hostInfos[env] = info
	return info, nil
}

// hostUsers returns the normal users of the host, sorted, as the config defines
// them. The list is read on every run, never stored. It fails when a name
// does not fit the nix-secrets layout.
func hostUsers(ctx context.Context, env *pipeline.Env) ([]string, error) {
	info, err := hostInfo(ctx, env)
	if err != nil {
		return nil, err
	}
	users := slices.Sorted(maps.Keys(info.Users))
	if err := secrets.CheckNames(env.Host, users); err != nil {
		return nil, err
	}
	return users, nil
}
