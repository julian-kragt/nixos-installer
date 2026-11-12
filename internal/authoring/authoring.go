// Package authoring writes a secret file from its template: the operator
// fills in the template in vim and types each password, which is hashed, and
// sops encrypts the result. It runs in the console helper on the target, so
// the plaintext only ever exists there.
package authoring

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"installer/internal/commands/mkpasswd"
	"installer/internal/commands/sops"
	"installer/internal/commands/vim"
	"installer/internal/machine"
	"installer/internal/runner"
	"installer/internal/secrets"
)

// File is a secret file to write: its path and its template's, both relative
// to the repository, and the user it belongs to, "" for the host's own.
type File struct {
	Path     string
	Template string
	User     string
}

// Arg is f as one argument of the console helper: path=template[:user].
// Neither a path of the nix-secrets layout nor a user name holds = or :.
func (f File) Arg() string {
	arg := f.Path + "=" + f.Template
	if f.User != "" {
		arg += ":" + f.User
	}
	return arg
}

// ParseArg is the inverse of Arg.
func ParseArg(arg string) (File, error) {
	path, rest, ok := strings.Cut(arg, "=")
	if !ok || path == "" || rest == "" {
		return File{}, fmt.Errorf("%q is not path=template[:user]", arg)
	}
	template, user, _ := strings.Cut(rest, ":")
	return File{Path: path, Template: template, User: user}, nil
}

// Author writes f in the repository at dir on m, encrypted to the recipients
// its .sops.yaml gives it. host names the host in the password prompts.
func Author(ctx context.Context, m machine.Machine, dir, host string, f File) error {
	data, err := os.ReadFile(filepath.Join(dir, f.Template))
	if err != nil {
		return err
	}
	tmpl, err := secrets.ParseTemplate(data)
	if err != nil {
		return fmt.Errorf("%s: %w", f.Template, err)
	}

	content := tmpl.Content()
	if tmpl.NeedsEditor() {
		buffer, err := tmpl.Buffer(f.Path)
		if err != nil {
			return err
		}
		// content is what the operator saved once check accepts it.
		check := func(b []byte) (err error) { content, err = tmpl.CheckEdited(b); return err }
		if _, err := vim.EditUntil(ctx, filepath.Base(f.Path), buffer, check); err != nil {
			return fmt.Errorf("%s: %w", f.Path, err)
		}
	}
	var hashes []secrets.Assignment
	for _, path := range tmpl.Passwords() {
		pw, err := secrets.AskValue(ctx, label(host, f, path))
		if err != nil {
			return err
		}
		hash, err := mkpasswd.Hash(ctx, m, pw)
		if err != nil {
			return err
		}
		hashes = append(hashes, secrets.Assignment{Path: path, Value: hash})
	}

	doc, err := secrets.Document(content, hashes...)
	if err != nil {
		return err
	}
	if err := (sops.Repo{Machine: m, Dir: dir}).Encrypt(ctx, f.Path, doc); err != nil {
		return err
	}
	runner.Info(ctx, "Encrypted "+f.Path)
	return nil
}

// label is what the operator is asked for the password at path of f.
func label(host string, f File, path secrets.Path) string {
	if f.User != "" && len(path) == 1 {
		return "Login password for " + f.User + " on " + host
	}
	return path.String() + " for " + f.Path
}
