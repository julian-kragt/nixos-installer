package stages

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"

	"github.com/julian-kragt/nixos-installer/internal/authoring"
	"github.com/julian-kragt/nixos-installer/internal/commands/hostidentity"
	"github.com/julian-kragt/nixos-installer/internal/commands/sops"
	"github.com/julian-kragt/nixos-installer/internal/console"
	"github.com/julian-kragt/nixos-installer/internal/pipeline"
	"github.com/julian-kragt/nixos-installer/internal/runner"
	"github.com/julian-kragt/nixos-installer/internal/secrets"
)

// authoringDir is the copy of the secrets repository on the target the console
// helper writes the files in. It only ever holds public keys, templates and
// ciphertext; the plaintext stays in the helper.
var authoringDir = path.Join(console.Dir, "repo")

// templateAuthoring makes every secret file of the host hold real values,
// encrypted to the recipients .sops.yaml gives it. The operator writes the
// files that are not fine yet at the target's console: the helper there opens
// the template in vim, asks for the passwords, hashes them and encrypts the
// file with sops to public keys. The provider only reads back the ciphertext,
// and the target, which holds the host's key, checks each file.
func templateAuthoring(ctx context.Context, env *pipeline.Env) error {
	users, err := hostUsers(ctx, env)
	if err != nil {
		return err
	}
	info, err := hostInfo(ctx, env)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(env.SecretsDir, secrets.ConfigFile))
	if err != nil {
		return err
	}
	cfg, err := secrets.ParseConfig(data)
	if err != nil {
		return err
	}

	files := []authoring.File{{Path: secrets.HostFile(env.Host), Template: secrets.HostTemplate}}
	for _, u := range users {
		files = append(files, authoring.File{Path: secrets.UserFile(env.Host, u), Template: secrets.UserTemplate, User: u})
	}
	var todo []authoring.File
	for _, f := range files {
		err := runner.Run(ctx, "Check "+f.Path, func(ctx context.Context) error {
			problem, err := fileProblem(ctx, env, cfg, f)
			if err != nil || problem == "" {
				return err
			}
			runner.Info(ctx, f.Path+": "+problem)
			todo = append(todo, f)
			return nil
		})
		if err != nil {
			return err
		}
	}
	if len(todo) == 0 {
		return nil
	}

	var session console.Session
	err = runner.RunSteps(ctx,
		runner.Step{Title: "Install console helper", Fn: func(ctx context.Context) (err error) {
			session, err = console.Prepare(ctx, env.Local, env.Remote, env.Host, info.System)
			return err
		}},
		runner.Step{Title: "Copy recipients and templates", Fn: func(ctx context.Context) error {
			return stageAuthoringDir(ctx, env)
		}},
	)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = env.Remote.Run(context.WithoutCancel(ctx), "rm", "-rf", authoringDir)
	}()

	args := []string{"secrets", env.Host, authoringDir}
	for _, f := range todo {
		args = append(args, f.Arg())
	}
	runErr := runner.Run(ctx, "Secrets at the console", func(ctx context.Context) error {
		return session.Run(ctx, "the secrets", args...)
	})
	if ctx.Err() != nil {
		return runErr
	}
	// The files the operator finished are kept, also when a later one was not.
	for _, f := range todo {
		err := runner.Run(ctx, "Import "+f.Path, func(ctx context.Context) error {
			return importFile(ctx, env, cfg, f)
		})
		if err != nil {
			return err
		}
	}
	return runErr
}

// stageAuthoringDir writes a fresh authoringDir on the target, with the
// repository's .sops.yaml and templates: public data only.
func stageAuthoringDir(ctx context.Context, env *pipeline.Env) error {
	if _, err := env.Remote.Run(ctx, "rm", "-rf", authoringDir); err != nil {
		return err
	}
	for _, f := range []string{secrets.ConfigFile, secrets.HostTemplate, secrets.UserTemplate} {
		data, err := os.ReadFile(filepath.Join(env.SecretsDir, f))
		if err != nil {
			return err
		}
		if err := console.WriteFile(ctx, env.Remote, path.Join(authoringDir, f), data, false); err != nil {
			return fmt.Errorf("copy %s to the target: %w", f, err)
		}
	}
	return nil
}

// importFile copies f, as the console helper wrote it, from the target into
// the secrets repository, once it is ciphertext, and checks it there. A file
// the helper did not write is left as it was.
func importFile(ctx context.Context, env *pipeline.Env, cfg *secrets.Config, f authoring.File) error {
	data, err := console.ReadFile(ctx, env.Remote, path.Join(authoringDir, f.Path))
	if err != nil {
		return err
	}
	if data == nil {
		runner.Warn(ctx, f.Path+" was not written at the console; it is left as it was")
		return nil
	}
	if err := sops.CheckCiphertext(data); err != nil {
		return fmt.Errorf("%s from the target: %w; it was not imported", f.Path, err)
	}
	local := filepath.Join(env.SecretsDir, f.Path)
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(local, data, 0o644); err != nil {
		return err
	}
	problem, err := fileProblem(ctx, env, cfg, f)
	if err != nil {
		return err
	}
	if problem != "" {
		return fmt.Errorf("%s after encrypting: %s", f.Path, problem)
	}
	runner.Info(ctx, "Imported "+f.Path+"; the target decrypts it")
	return nil
}

// fileProblem is sops.Problem for f in the secrets repo, checked with the
// host's key on the target against f's template and recipients.
func fileProblem(ctx context.Context, env *pipeline.Env, cfg *secrets.Config, f authoring.File) (string, error) {
	data, err := os.ReadFile(filepath.Join(env.SecretsDir, f.Template))
	if err != nil {
		return "", err
	}
	tmpl, err := secrets.ParseTemplate(data)
	if err != nil {
		return "", fmt.Errorf("%s: %w", f.Template, err)
	}
	want, err := cfg.Recipients(f.Path)
	if err != nil {
		return "", err
	}
	ciphertext, err := os.ReadFile(filepath.Join(env.SecretsDir, f.Path))
	if errors.Is(err, os.ErrNotExist) {
		ciphertext, err = nil, nil
	}
	if err != nil {
		return "", err
	}
	return sops.Problem(ctx, env.Remote, hostidentity.KeyFile, ciphertext, want, tmpl.Keys)
}
