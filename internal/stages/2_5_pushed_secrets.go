package stages

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/julian-kragt/nixos-installer/internal/commands/sops"
	"github.com/julian-kragt/nixos-installer/internal/pipeline"
	"github.com/julian-kragt/nixos-installer/internal/runner"
	"github.com/julian-kragt/nixos-installer/internal/secrets"
)

// pushedSecrets checks nix-secrets as the host will build it: at
// SecretsCommit, read from git, not from the clone's files. It runs before 3.1
// locks the commit into the config, so a bad commit is never deployed. The
// commit must be on the remote, have the layout 2.1 recorded, and hold exactly
// the host's files for the users of the config, each encrypted to the
// recipients of its own rule, as are the shared files its users left.
// Nothing is decrypted. It reports every problem, then reopens 2.1 and every
// stage after it, and fails: the next run checks the clone again, rewrites
// what it can and locks and deploys the new commit. An
// error that is not a problem of the commit, as a failed fetch, reopens
// nothing, so the next run only checks again.
func pushedSecrets(ctx context.Context, env *pipeline.Env) error {
	commit := env.State.SecretsCommit
	if commit == "" {
		return errors.New("no nix-secrets commit is recorded; run the secrets phase first")
	}
	var problems []string
	err := runner.Run(ctx, fmt.Sprintf("Check nix-secrets at %.7s", commit), func(ctx context.Context) error {
		users, err := hostUsers(ctx, env)
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
		if err := reopenFrom(env, stageSecretsRepository); err != nil {
			return fmt.Errorf("persist state: %w", err)
		}
		return fmt.Errorf("nix-secrets at %.7s does not hold %s's secrets as it should:\n  %s\n"+
			"the next run repeats stage %s and every stage after it, which fix the clone at %s and push it again; "+
			"fix by hand what they cannot, then run the installer again",
			commit, env.Host, strings.Join(problems, "\n  "), stageSecretsRepository, env.SecretsDir)
	}
	runner.Info(ctx, fmt.Sprintf("nix-secrets at %.7s holds %s's secrets as it should", commit, env.Host))
	return nil
}

// pushedProblems returns what is wrong with the host's secrets at commit, one
// line each. A problem that keeps one check from running does not keep the
// others from running.
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

	layout, err := secretsLayout(env)
	if err != nil {
		return nil, err
	}
	switch at, err := secretsFlake(env).At(commit).SecretsLayout(ctx); {
	case err != nil:
		problems = append(problems, err.Error())
	case at != layout:
		problems = append(problems, fmt.Sprintf("secretsEntry: the layout is %+v, not %+v as when the install started", at, layout))
	}

	paths, err := repo.Files(ctx, commit, append([]string{secrets.ConfigFile}, layout.Dirs()...)...)
	if err != nil {
		return nil, err
	}
	var cfg *secrets.Config
	if !slices.Contains(paths, secrets.ConfigFile) {
		problems = append(problems, secrets.ConfigFile+": missing")
	} else {
		data, err := repo.Show(ctx, commit, secrets.ConfigFile)
		if err != nil {
			return nil, err
		}
		if cfg, err = secrets.ParseConfig(data); err != nil {
			problems = append(problems, err.Error())
		}
	}

	files := layout.Files(env.Host, users)
	if cfg != nil {
		shadowed, err := cfg.Shadowed(files)
		if err != nil {
			problems = append(problems, err.Error())
		}
		for _, s := range shadowed {
			problems = append(problems, s.String())
		}
	}
	for _, f := range files {
		var ciphertext []byte // nil is "missing"
		if slices.Contains(paths, f) {
			if ciphertext, err = repo.Show(ctx, commit, f); err != nil {
				return nil, err
			}
		}
		problem, err := fileProblemAt(cfg, f, ciphertext)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		if problem != "" {
			problems = append(problems, f+": "+problem)
		}
	}

	for _, f := range paths {
		if u, ok := layout.FileUser(env.Host, f); ok && !slices.Contains(users, u) {
			problems = append(problems, fmt.Sprintf("%s: %s is no longer a user of %s", f, u, env.Host))
		}
	}

	// A shared file a user left goes to its rule's other recipients, or is
	// gone with its rule.
	for _, f := range env.State.LeftShared {
		if slices.Contains(files, f) {
			continue
		}
		var ciphertext []byte
		if slices.Contains(paths, f) {
			if ciphertext, err = repo.Show(ctx, commit, f); err != nil {
				return nil, err
			}
		}
		switch {
		case cfg == nil:
		case !cfg.HasRule(f) && ciphertext != nil:
			problems = append(problems, f+": nobody reads it any more, but it is still there")
		case cfg.HasRule(f):
			problem, err := fileProblemAt(cfg, f, ciphertext)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}
			if problem != "" {
				problems = append(problems, f+": "+problem)
			}
		}
	}
	return problems, nil
}

// fileProblemAt says what is wrong with a secret file of the commit, or "".
// Without a usable cfg, or a rule that gives the file recipients, only what
// needs no recipients is checked: the file exists and has only ENC values.
func fileProblemAt(cfg *secrets.Config, file string, ciphertext []byte) (string, error) {
	var want []string
	var recipientsErr error
	if cfg != nil {
		want, recipientsErr = cfg.Recipients(file)
	}
	if cfg == nil || recipientsErr != nil {
		return joinProblems(sops.ShapeProblem(ciphertext), recipientsErr), nil
	}
	return sops.StaticProblem(ciphertext, want)
}

// joinProblems is problem and err in one line, each when there is one.
func joinProblems(problem string, err error) string {
	switch {
	case err == nil:
		return problem
	case problem == "":
		return err.Error()
	}
	return problem + "; " + err.Error()
}
