// Package stages holds the stages of the install pipeline, one file per stage.
// A stage file orchestrates what happens in that stage: the logic lives in the
// packages it calls.
package stages

import (
	"context"
	"maps"
	"slices"

	"github.com/julian-kragt/nixos-installer/internal/commands/git"
	"github.com/julian-kragt/nixos-installer/internal/commands/nix"
	"github.com/julian-kragt/nixos-installer/internal/pipeline"
	"github.com/julian-kragt/nixos-installer/internal/secrets"
)

// IDs of the stages that other stages refer to.
const (
	// stageBaseInstall is the stage after which the target's system must not
	// change any more.
	stageBaseInstall       = "0.2"
	stageSecretsRepository = "2.1"
	stageRecipients        = "2.2"
	stageCommitPush        = "2.4"
	stagePushedSecrets     = "2.5"
	stageFlakeInput        = "3.1"
)

// order holds the IDs of All in order. init fills it: All refers to the
// stages, which refer to it through reopenFrom.
var order []string

func init() {
	for _, s := range All {
		order = append(order, s.ID)
	}
}

// reopenFrom drops the stage id and every stage after it from the completed
// stages and saves the state, as pipeline.Run does for an outdated stage, so
// nothing that depends on what the stage writes stays recorded as done.
func reopenFrom(env *pipeline.Env, id string) error {
	i := slices.Index(order, id)
	if i < 0 {
		panic("stages: no stage " + id)
	}
	return env.State.Reopen(order[i:]...)
}

// BaseInstalled reports whether st records the base install: until then the
// target is to run the NixOS ISO, prepared as the instructions say.
func BaseInstalled(st *pipeline.State) bool { return st.IsCompleted(stageBaseInstall) }

// All is the pipeline: the stages in the order they run.
var All = []pipeline.Stage{
	{ID: "0.0", Name: "Environment preparation", Always: true, Run: environmentPreparation},
	{ID: "0.1", Name: "Provider preparation", Run: providerPreparation},
	{ID: stageBaseInstall, Name: "Base install", Run: baseInstall},
	{ID: "1.1", Name: "Readiness", Run: readiness},
	{ID: "1.2", Name: "Host identity", Run: hostIdentity, Outdated: hostIdentityOutdated},
	{ID: "1.3", Name: "User identity", Run: userIdentity, Outdated: userIdentityOutdated},
	{ID: stageSecretsRepository, Name: "Secrets repository", Run: secretsRepository},
	{ID: stageRecipients, Name: "Recipients", Run: recipients},
	{ID: "2.3", Name: "Template authoring", Run: templateAuthoring},
	{ID: stageCommitPush, Name: "Commit and push", Run: commitPush, Outdated: commitPushOutdated},
	{ID: stagePushedSecrets, Name: "Pushed secrets", Run: pushedSecrets},
	{ID: stageFlakeInput, Name: "Flake input", Run: flakeInput, Outdated: flakeInputOutdated},
	{ID: "3.2", Name: "Rebuild", Run: rebuild},
	{ID: "3.3", Name: "Cleanup", Run: cleanup},
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
	return usersOf(env.Host, info)
}

// usersOf returns the normal users in info, sorted. It fails when a name does
// not fit the nix-secrets layout.
func usersOf(host string, info nix.HostInfo) ([]string, error) {
	users := slices.Sorted(maps.Keys(info.Users))
	if err := secrets.CheckNames(host, users); err != nil {
		return nil, err
	}
	return users, nil
}
