// Package luks puts the LUKS passphrase on the target, where the config's
// disko.nix reads it during the install. The operator types it at the
// target's console, so the provider never sees it.
package luks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"installer/internal/console"
	"installer/internal/machine"
	"installer/internal/runner"
)

// passwordPath is where the passphrase lives on the target. A variable so tests
// can point it elsewhere.
var passwordPath = "/tmp/disko-password"

// Ask has the operator type the passphrase at the console of the session's
// target, where the helper writes it to passwordPath.
func Ask(ctx context.Context, s console.Session) error {
	return s.Run(ctx, "the LUKS passphrase", "luks", s.Host)
}

// Present reports whether the passphrase is on target. After a reboot of the
// ISO it is not.
func Present(ctx context.Context, target machine.Machine) (bool, error) {
	_, err := target.Run(ctx, "test", "-s", passwordPath)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return err == nil, err
}

// Remove deletes passwordPath on target and reports whether it was there.
// After the install it must not be: the passphrase unlocks the disk it would
// be on. rm, not shred: the target's /tmp is on a copy-on-write filesystem.
func Remove(ctx context.Context, target machine.Machine) (bool, error) {
	out, err := target.Run(ctx, "sh", "-c", `if [ -e "$1" ]; then rm -f "$1" && echo removed; fi`, "sh", passwordPath)
	if err != nil {
		return false, fmt.Errorf("remove %s: %w", passwordPath, err)
	}
	return strings.TrimSpace(out) == "removed", nil
}

// Type runs in the console helper on the target: it asks for the passphrase
// until two non-empty answers agree and writes it, without a newline, to
// passwordPath (mode 600). The passphrase is only ever in memory and in that
// file: not in args, the environment or a log.
func Type(ctx context.Context) error {
	pw, err := askPassphrase(ctx)
	if err != nil {
		return err
	}
	if err := os.Remove(passwordPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(passwordPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(pw); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// askPassphrase asks until two non-empty answers agree.
func askPassphrase(ctx context.Context) (string, error) {
	for {
		first, second, err := runner.AskSecretTwice(ctx, "LUKS passphrase: ", "Repeat LUKS passphrase: ")
		if err != nil {
			return "", fmt.Errorf("LUKS passphrase: %w", err)
		}
		switch {
		case first == "":
			runner.Warn(ctx, "The passphrase cannot be empty")
		case first != second:
			runner.Warn(ctx, "The passphrases do not match")
		default:
			return first, nil
		}
	}
}
