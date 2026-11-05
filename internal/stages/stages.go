// Package stages holds the stages of the install pipeline, one file per stage.
// A stage file orchestrates what happens in that stage: the logic lives in the
// packages it calls.
package stages

import (
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
}

// configFlake is the config flake, evaluated on the provider.
func configFlake(env *pipeline.Env) nix.Config {
	return nix.Config{Machine: env.Local, Dir: env.ConfigDir}
}

// configRepo is the git repository of the config flake, on the provider.
func configRepo(env *pipeline.Env) git.Repo {
	return git.Repo{Machine: env.Local, Dir: env.ConfigDir}
}
