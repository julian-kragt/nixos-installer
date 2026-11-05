// Package hostidentity gives a target and each of its users an age key of their
// own: the keys that decrypt the host's and the users' secrets.
package hostidentity

import (
	"context"
	"fmt"
	"path"
	"strings"

	"installer/internal/machine"
)

// KeyFile is where the config's sops-nix looks for the host's age key.
const KeyFile = "/var/lib/sops-nix/key.txt"

// UserKeyFile is where a user's sops, and Home Manager's sops-nix, look for the
// user's age key, relative to the user's home.
const UserKeyFile = ".config/sops/age/keys.txt"

// ensureKey creates the key file $1 unless it exists and prints its recipient. The
// private key stays in the file: age-keygen -o writes it there, -y prints the public key.
const ensureKey = `mkdir -p "$(dirname "$1")" && { test -e "$1" || age-keygen -o "$1"; } && age-keygen -y "$1"`

// Recipient makes sure target has an age key at path, generating one there when
// it is missing, and returns its recipient. An existing key is never replaced.
func Recipient(ctx context.Context, target machine.Machine, path string) (string, error) {
	recipient, err := ensure(ctx, target, "sh", "-c", ensureKey, "sh", path)
	if err != nil {
		return "", fmt.Errorf("host age key at %s: %w", path, err)
	}
	return recipient, nil
}

// UserRecipient is Recipient for user, whose home on target is home: the key is
// <home>/UserKeyFile, created by the user, so the user owns it. runuser keeps
// root's HOME, so the path comes from home, not from $HOME.
func UserRecipient(ctx context.Context, target machine.Machine, user, home string) (string, error) {
	file := path.Join(home, UserKeyFile)
	recipient, err := ensure(ctx, target, "runuser", "-u", user, "--", "sh", "-c", ensureKey, "sh", file)
	if err != nil {
		return "", fmt.Errorf("age key of %s at %s: %w", user, file, err)
	}
	return recipient, nil
}

// ensure runs the command that ensures a key and returns the recipient it printed.
func ensure(ctx context.Context, target machine.Machine, name string, args ...string) (string, error) {
	out, err := target.Quiet().Run(ctx, name, args...)
	if err != nil {
		return "", err
	}
	recipient := strings.TrimSpace(out)
	if !strings.HasPrefix(recipient, "age1") {
		return "", fmt.Errorf("age-keygen printed something that is not an age recipient")
	}
	return recipient, nil
}
