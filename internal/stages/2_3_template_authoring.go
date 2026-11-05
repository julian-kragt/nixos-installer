package stages

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"installer/internal/commands/hostidentity"
	"installer/internal/commands/mkpasswd"
	"installer/internal/commands/sops"
	"installer/internal/commands/vim"
	"installer/internal/pipeline"
	"installer/internal/runner"
	"installer/internal/secrets"
)

// templateAuthoring makes every secret file of the host hold real values,
// encrypted to the recipients .sops.yaml gives it. The operator writes a file
// that does not in vim and types its passwords; sops on the provider encrypts
// them to public keys only. The target, which holds the host's key, checks
// each file, so nothing is ever decrypted on the provider.
func templateAuthoring(ctx context.Context, env *pipeline.Env) error {
	users, err := hostUsers(ctx, env)
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
	if err := authorFile(ctx, env, cfg, secrets.HostFile(env.Host), secrets.HostTemplate, ""); err != nil {
		return err
	}
	for _, u := range users {
		if err := authorFile(ctx, env, cfg, secrets.UserFile(env.Host, u), secrets.UserTemplate, u); err != nil {
			return err
		}
	}
	return nil
}

// authorFile writes file again unless it is fine already: the operator fills
// in the template in vim when it has values to write, and types each password,
// which is hashed. user is the user the file belongs to, "" for the host's own.
func authorFile(ctx context.Context, env *pipeline.Env, cfg *secrets.Config, file, template, user string) error {
	return runner.Run(ctx, "Check "+file, func(ctx context.Context) error {
		data, err := os.ReadFile(filepath.Join(env.SecretsDir, template))
		if err != nil {
			return err
		}
		tmpl, err := secrets.ParseTemplate(data)
		if err != nil {
			return fmt.Errorf("%s: %w", template, err)
		}
		want, err := cfg.Recipients(file)
		if err != nil {
			return err
		}
		problem, err := fileProblem(ctx, env, file, want, tmpl.Keys)
		if err != nil || problem == "" {
			return err
		}
		runner.Info(ctx, file+": "+problem)

		content := tmpl.Content()
		if tmpl.NeedsEditor() {
			buffer, err := tmpl.Buffer(file)
			if err != nil {
				return err
			}
			// content is what the operator saved once check accepts it.
			check := func(b []byte) (err error) { content, err = tmpl.CheckEdited(b); return err }
			if _, err := vim.EditUntil(ctx, filepath.Base(file), buffer, check); err != nil {
				return fmt.Errorf("%s: %w", file, err)
			}
		}
		var hashes []secrets.Assignment
		for _, path := range tmpl.Passwords() {
			label := path.String() + " for " + file
			if user != "" && len(path) == 1 {
				label = "Login password for " + user + " on " + env.Host
			}
			pw, err := secrets.AskValue(ctx, label)
			if err != nil {
				return err
			}
			hash, err := mkpasswd.Hash(ctx, env.Local, pw)
			if err != nil {
				return err
			}
			hashes = append(hashes, secrets.Assignment{Path: path, Value: hash})
		}

		doc, err := secrets.Document(content, hashes...)
		if err != nil {
			return err
		}
		if err := (sops.Repo{Machine: env.Local, Dir: env.SecretsDir}).Encrypt(ctx, file, doc); err != nil {
			return err
		}
		problem, err = fileProblem(ctx, env, file, want, tmpl.Keys)
		if err != nil {
			return err
		}
		if problem != "" {
			return fmt.Errorf("%s after encrypting: %s", file, problem)
		}
		runner.Info(ctx, "Encrypted "+file+"; the target decrypts it")
		return nil
	})
}

// fileProblem is sops.Problem for file in the secrets repo, checked with the
// host's key on the target.
func fileProblem(ctx context.Context, env *pipeline.Env, file string, want []string, keys []secrets.TemplateKey) (string, error) {
	ciphertext, err := os.ReadFile(filepath.Join(env.SecretsDir, file))
	if errors.Is(err, os.ErrNotExist) {
		ciphertext, err = nil, nil
	}
	if err != nil {
		return "", err
	}
	return sops.Problem(ctx, env.Remote, hostidentity.KeyFile, ciphertext, want, keys)
}
