package stages

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"installer/internal/commands/sops"
	"installer/internal/pipeline"
	"installer/internal/runner"
	"installer/internal/secrets"
)

// pushedSecrets checks nix-secrets as the host builds it: at SecretsCommit,
// read from git, not from the clone's files. The commit must be on the remote,
// and hold exactly the host's files for the users of the config at ConfigRev,
// each encrypted to the recipients of its own rule. Nothing is decrypted. It
// reports every problem, then fails.
func pushedSecrets(ctx context.Context, env *pipeline.Env) error {
	commit := env.State.SecretsCommit
	if commit == "" {
		return errors.New("no nix-secrets commit is recorded; run the secrets phase first")
	}
	if env.State.ConfigRev == "" {
		return errors.New("no config revision is recorded; run stage 3.1 first")
	}
	var problems []string
	err := runner.Run(ctx, fmt.Sprintf("Check nix-secrets at %.7s", commit), func(ctx context.Context) error {
		users, err := hostUsersAt(ctx, env, env.State.ConfigRev)
		if err != nil {
			return err
		}
		problems, err = pushedProblems(ctx, env, commit, users)
		return err
	})
	if err != nil {
		return err
	}
	if len(problems) > 0 {
		return fmt.Errorf("nix-secrets at %.7s does not hold %s's secrets as it should:\n  %s",
			commit, env.Host, strings.Join(problems, "\n  "))
	}
	runner.Info(ctx, fmt.Sprintf("nix-secrets at %.7s holds %s's secrets as it should", commit, env.Host))
	return nil
}

// pushedProblems returns what is wrong with the host's secrets at commit, one
// line each.
func pushedProblems(ctx context.Context, env *pipeline.Env, commit string, users []string) ([]string, error) {
	repo := secretsRepo(env)
	var problems []string
	pushed, err := repo.OnUpstream(ctx, commit)
	if err != nil {
		return nil, err
	}
	if !pushed {
		problems = append(problems, fmt.Sprintf("%.7s is not on the upstream of %s", commit, env.SecretsDir))
	}

	data, ok, err := repo.Show(ctx, commit, secrets.ConfigFile)
	if err != nil {
		return nil, err
	}
	if !ok {
		return append(problems, secrets.ConfigFile+": missing"), nil
	}
	cfg, err := secrets.ParseConfig(data)
	if err != nil {
		return nil, err
	}
	files := secrets.Files(env.Host, users)
	shadowed, err := cfg.Shadowed(files)
	if err != nil {
		return nil, err
	}
	for _, s := range shadowed {
		problems = append(problems, s.String())
	}
	for _, f := range files {
		want, err := cfg.Recipients(f)
		if err != nil {
			problems = append(problems, f+": "+err.Error())
			continue
		}
		// A file the commit does not have is nil, which is "missing".
		ciphertext, _, err := repo.Show(ctx, commit, f)
		if err != nil {
			return nil, err
		}
		problem, err := sops.StaticProblem(ciphertext, want)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		if problem != "" {
			problems = append(problems, f+": "+problem)
		}
	}

	userFiles, err := repo.Files(ctx, commit, "host-users")
	if err != nil {
		return nil, err
	}
	for _, f := range userFiles {
		if u, ok := secrets.FileUser(env.Host, f); ok && !slices.Contains(users, u) {
			problems = append(problems, fmt.Sprintf("%s: %s is no longer a user of %s", f, u, env.Host))
		}
	}
	return problems, nil
}
