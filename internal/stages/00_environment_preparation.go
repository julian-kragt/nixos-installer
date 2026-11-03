package stages

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

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
		runner.Step{Title: "Set up SSH access", Fn: func(ctx context.Context) error {
			return setupSSHAccess(ctx, env)
		}},
		runner.Step{Title: "Check config flake", Fn: func(context.Context) error {
			return checkConfig(env.ConfigDir)
		}},
	)
}

// setupSSHAccess makes key auth to the target work. When it does not yet, the
// provider's public keys are installed with the root password, but only while
// the target is still the ISO: once the base install (0.2) is done the root
// password is gone and a failed key login is an error.
func setupSSHAccess(ctx context.Context, env *pipeline.Env) error {
	_, keyAuthErr := env.Remote.Run(ctx, "true")
	if keyAuthErr == nil {
		return nil // key auth already works
	}
	if !isSSHAuthError(keyAuthErr) || slices.Contains(env.State.Completed, "0.2") {
		return keyAuthErr
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
	err = pipeline.BootstrapKey(ctx, env.Target, password, keys)
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

const passwordRejectedHint = "Hint: the target rejected password auth. Set a root password first:\n  passwd root"

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
