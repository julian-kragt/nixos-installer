// Package luks puts the LUKS passphrase on the target, where the config's
// disko.nix reads it during the install.
package luks

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"installer/internal/machine"
	"installer/internal/runner"
)

// passwordPath is where the passphrase lives on the target. A variable so tests
// can point it elsewhere.
var passwordPath = "/tmp/disko-password"

// AskAndSend asks for the passphrase (twice) and puts it in passwordPath on
// target. The passphrase is only ever in memory, on stdin of a quiet command and
// in that file: not in the environment (every child process sees it), not in
// args, not in the log.
func AskAndSend(ctx context.Context, target machine.Machine) error {
	pw, err := askPassphrase(ctx)
	if err != nil {
		return err
	}
	return sendPassphrase(ctx, target, pw)
}

// Ensure asks for the passphrase again when it is not on target any more, as
// after a reboot of the ISO.
func Ensure(ctx context.Context, target machine.Machine) error {
	_, err := target.Run(ctx, "test", "-s", passwordPath)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return AskAndSend(ctx, target)
	}
	return err
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

// sendPassphrase writes pw, without a newline, to passwordPath (mode 600) on
// target.
func sendPassphrase(ctx context.Context, target machine.Machine, pw string) error {
	_, err := target.Quiet().RunStdin(ctx, strings.NewReader(pw),
		"sh", "-c", `rm -f "$1" && umask 077 && cat > "$1"`, "sh", passwordPath)
	if err != nil {
		return fmt.Errorf("send LUKS passphrase: %w", err)
	}
	return nil
}
