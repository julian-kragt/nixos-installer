package stages

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"installer/internal/pipeline"
	"installer/internal/runner"
)

// environmentPreparation checks the provider machine and the target before
// anything touches the target. It runs on every invocation.
func environmentPreparation(ctx context.Context, env *pipeline.Env) error {
	return runner.RunSteps(ctx,
		runner.Step{Title: "Check local tools", Fn: checkLocalTools},
		runner.Step{Title: "Check internet connection", Fn: func(ctx context.Context) error {
			return dialTCP(ctx, "8.8.8.8:53")
		}},
		runner.Step{Title: "Check target is reachable", Fn: func(ctx context.Context) error {
			return dialTCP(ctx, net.JoinHostPort(env.Target, "22"))
		}},
		runner.Step{Title: "Check SSH host key", Fn: func(ctx context.Context) error {
			offered, err := pipeline.ScanHostKey(ctx, env.Target)
			if err != nil {
				return fmt.Errorf("read the host key of %s: %w", env.Target, err)
			}
			return checkHostKey(ctx, env, offered)
		}},
		runner.Step{Title: "Set up SSH access", Fn: func(ctx context.Context) error {
			return setupSSHAccess(ctx, env)
		}},
		runner.Step{Title: "Check config flake", Fn: func(context.Context) error {
			return checkConfig(env.ConfigDir)
		}},
	)
}

// checkHostKey pins the key the target offers at first contact, so nothing is
// sent to a swapped target later on. A different key is refused once the base
// install (0.2) is done. Before that the operator can accept it, because the
// key changes for ordinary reasons: every ISO boot makes a new one, and an
// interrupted 0.2 may already have installed the system.
func checkHostKey(ctx context.Context, env *pipeline.Env, offered ssh.PublicKey) error {
	pinned, err := env.Pin.Load()
	if err != nil {
		return err
	}
	if pinned == nil {
		runner.Info(ctx, fmt.Sprintf("Pinned the host key of %s: %s", env.Target, pipeline.Fingerprint(offered)))
		return env.Pin.Save(offered)
	}
	if pipeline.SameKey(pinned, offered) {
		return nil
	}

	change := fmt.Sprintf("the host key of %s changed\n  pinned: %s\n  target: %s",
		env.Target, pipeline.Fingerprint(pinned), pipeline.Fingerprint(offered))
	if slices.Contains(env.State.Completed, "0.2") {
		return fmt.Errorf("%s\nthis is not the system that was installed; if you reinstalled it on purpose, remove %s and %s to start over",
			change, env.Pin.File, env.State.Path())
	}
	runner.Warn(ctx, change)
	answer, err := runner.Ask(ctx, "If you rebooted the ISO or the install was interrupted, type 'trust' to accept the new key: ")
	if errors.Is(err, io.EOF) {
		return errors.New(change)
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(answer) != "trust" {
		return errors.New("aborted: the new host key was not accepted")
	}
	return env.Pin.Save(offered)
}

// setupSSHAccess makes key auth to the target work. When it does not yet, the
// provider's public keys are installed with the root password, but only on an
// ISO that offers password login. The installed system takes only the keys in
// rootAuthorizedKeys, so there a failed key login is an error, also when an
// interrupted base install (0.2) was not recorded.
func setupSSHAccess(ctx context.Context, env *pipeline.Env) error {
	_, keyAuthErr := env.Remote.Run(ctx, "true")
	if keyAuthErr == nil {
		return nil // key auth already works
	}
	if !isSSHAuthError(keyAuthErr) {
		return keyAuthErr
	}
	if slices.Contains(env.State.Completed, "0.2") || !offersPassword(keyAuthErr) {
		return fmt.Errorf("%w\n\n%s", keyAuthErr, keyOnlyHint)
	}

	password := env.SSHPassword
	if password == "" {
		var err error
		password, err = runner.AskSecret(ctx, "SSH password for root@"+env.Target+": ")
		if err != nil {
			return fmt.Errorf("SSH password: %w", err)
		}
		if password == "" {
			return keyAuthErr
		}
	}
	keys, err := collectProviderKeys(ctx, env.Local)
	if err != nil {
		return err
	}
	err = pipeline.InstallAuthorizedKeys(ctx, env.Target, password, keys, env.Pin)
	if errors.Is(err, pipeline.ErrAuth) {
		return fmt.Errorf("%w\n\n%s", err, passwordRejectedHint)
	}
	if err != nil {
		return err
	}
	if _, err := env.Remote.Run(ctx, "true"); err != nil {
		return fmt.Errorf("key auth after install: %w", err)
	}
	return nil
}

const (
	passwordRejectedHint = "Hint: the target rejected password auth. Set a root password first:\n  passwd root"
	keyOnlyHint          = "Hint: root on the target logs in with a key only. Load one of the host's rootAuthorizedKeys:\n  ssh-add"
)

// collectProviderKeys returns the SSH public keys the provider should use,
// preferring keys loaded in the SSH agent (ssh-add -L), with a fallback to
// standard key files under ~/.ssh.
func collectProviderKeys(ctx context.Context, local pipeline.Machine) (string, error) {
	out, err := local.Run(ctx, "ssh-add", "-L")
	trimmed := strings.TrimSpace(out)
	// Some agent emulations exit 0 with "The agent has no identities." — treat
	// that the same as an error and fall through to the file fallback.
	if err == nil && trimmed != "" && !strings.HasPrefix(trimmed, "The agent has no identities") {
		return out, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	var keys strings.Builder
	for _, name := range []string{"id_ed25519.pub", "id_rsa.pub", "id_ecdsa.pub"} {
		data, err := os.ReadFile(filepath.Join(home, ".ssh", name))
		if err == nil {
			keys.Write(data)
			if len(data) > 0 && data[len(data)-1] != '\n' {
				keys.WriteByte('\n')
			}
		}
	}
	if keys.Len() == 0 {
		return "", errors.New("no SSH public keys found in agent or ~/.ssh; load a key with ssh-add or create one")
	}
	return keys.String(), nil
}

// isSSHAuthError reports whether err is an SSH authentication failure (as
// opposed to a network or host-key error). OpenSSH always appends a
// parenthesised method list: "Permission denied (publickey,password)".
func isSSHAuthError(err error) bool {
	return strings.Contains(err.Error(), "Permission denied (")
}

// offersPassword reports whether the method list of an SSH authentication
// failure includes password, the method InstallAuthorizedKeys uses. The
// installed system lists only publickey.
func offersPassword(err error) bool {
	_, rest, ok := strings.Cut(err.Error(), "Permission denied (")
	if !ok {
		return false
	}
	methods, _, _ := strings.Cut(rest, ")")
	return slices.Contains(strings.Split(methods, ","), "password")
}

func dialTCP(ctx context.Context, addr string) error {
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	return conn.Close()
}

func checkLocalTools(context.Context) error {
	for _, tool := range []string{"nix", "ssh", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("%s not found in PATH", tool)
		}
	}
	return nil
}

func checkConfig(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "flake.nix")); err != nil {
		return fmt.Errorf("config %s: %w", dir, err)
	}
	return nil
}
