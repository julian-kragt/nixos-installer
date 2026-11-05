package machine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"installer/internal/runner"
)

// Access is how the installer gets to log in to a target: the host key it trusts
// and the key login it sets up.
type Access struct {
	Local     Machine // the provider, whose keys log in
	Remote    Machine // the target
	Addr      string  // IP address of the target
	Pin       HostPin // the host key Remote accepts
	Installed bool    // the base install is done, so the system must not change
}

// CheckHostKey checks the key the target offers against the pin.
func (a Access) CheckHostKey(ctx context.Context) error {
	offered, err := ScanHostKey(ctx, a.Addr)
	if err != nil {
		return fmt.Errorf("read the host key of %s: %w", a.Addr, err)
	}
	return a.checkOffered(ctx, offered)
}

// checkOffered pins the key the target offers at first contact, so nothing is
// sent to a swapped target later on. A different key is refused once the base
// install is done. Before that the operator can accept it, because the key
// changes for ordinary reasons: every ISO boot makes a new one, and an
// interrupted base install may already have installed the system.
func (a Access) checkOffered(ctx context.Context, offered ssh.PublicKey) error {
	pinned, err := a.Pin.Load()
	if err != nil {
		return err
	}
	if pinned == nil {
		runner.Info(ctx, fmt.Sprintf("Pinned the host key of %s: %s", a.Addr, Fingerprint(offered)))
		return a.Pin.Save(offered)
	}
	if SameKey(pinned, offered) {
		return nil
	}

	change := fmt.Sprintf("the host key of %s changed\n  pinned: %s\n  target: %s",
		a.Addr, Fingerprint(pinned), Fingerprint(offered))
	if a.Installed {
		return fmt.Errorf("%s\nthis is not the system that was installed; to install it again, boot the NixOS ISO and run with --reinstall", change)
	}
	runner.Warn(ctx, change)
	err = runner.RequireTyped(ctx, "If you rebooted the ISO or the install was interrupted, type 'trust' to accept the new key: ",
		"trust", errors.New(change), errors.New("aborted: the new host key was not accepted"))
	if err != nil {
		return err
	}
	return a.Pin.Save(offered)
}

// SetupKeyLogin makes key auth to the target work. When it does not yet, the
// provider's public keys are installed with the root password, but only on an
// ISO that offers password login. The installed system takes only the keys in
// rootAuthorizedKeys, so there a failed key login is an error, also when an
// interrupted base install was not recorded. An empty password is asked for.
func (a Access) SetupKeyLogin(ctx context.Context, password string) error {
	_, keyAuthErr := a.Remote.Run(ctx, "true")
	if keyAuthErr == nil {
		return nil // key auth already works
	}
	if !isAuthError(keyAuthErr) {
		return keyAuthErr
	}
	if a.Installed || !offersPassword(keyAuthErr) {
		return fmt.Errorf("%w\n\n%s", keyAuthErr, keyOnlyHint)
	}

	if password == "" {
		var err error
		password, err = runner.AskSecret(ctx, "SSH password for root@"+a.Addr+": ")
		if err != nil {
			return fmt.Errorf("SSH password: %w", err)
		}
		if password == "" {
			return keyAuthErr
		}
	}
	keys, err := providerKeys(ctx, a.Local)
	if err != nil {
		return err
	}
	err = InstallAuthorizedKeys(ctx, a.Addr, password, keys, a.Pin)
	if errors.Is(err, ErrAuth) {
		return fmt.Errorf("%w\n\n%s", err, passwordRejectedHint)
	}
	if err != nil {
		return err
	}
	if _, err := a.Remote.Run(ctx, "true"); err != nil {
		return fmt.Errorf("key auth after install: %w", err)
	}
	return nil
}

const (
	passwordRejectedHint = "Hint: the target rejected password auth. Set a root password first:\n  passwd root"
	keyOnlyHint          = "Hint: root on the target logs in with a key only. Load one of the host's rootAuthorizedKeys:\n  ssh-add"
)

// AgentKeys returns the public keys in m's ssh-agent, as `ssh-add -L` prints
// them. An agent without keys is an error.
func (m Machine) AgentKeys(ctx context.Context) (string, error) {
	out, err := m.Quiet().Run(ctx, "ssh-add", "-L")
	if err != nil {
		return "", err
	}
	// Some agent emulations exit 0 with "The agent has no identities."
	trimmed := strings.TrimSpace(out)
	if trimmed == "" || strings.HasPrefix(trimmed, "The agent has no identities") {
		return "", errors.New("the agent has no identities")
	}
	return out, nil
}

// providerKeys returns the SSH public keys the provider should use, preferring
// keys loaded in the SSH agent, with a fallback to standard key files under
// ~/.ssh.
func providerKeys(ctx context.Context, local Machine) (string, error) {
	if out, err := local.AgentKeys(ctx); err == nil {
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

// KeyAuthorized reports whether a key of `ssh-add -L` output is among the
// authorized_keys lines. Keys are compared by their base64 blob, so comments
// and options do not matter.
func KeyAuthorized(agent string, authorized []string) bool {
	blobs := map[string]bool{}
	for _, line := range strings.Split(agent, "\n") {
		if f := strings.Fields(line); len(f) >= 2 {
			blobs[f[1]] = true
		}
	}
	for _, line := range authorized {
		for _, field := range strings.Fields(line) {
			if blobs[field] {
				return true
			}
		}
	}
	return false
}

// isAuthError reports whether err is an SSH authentication failure (as
// opposed to a network or host-key error). OpenSSH always appends a
// parenthesised method list: "Permission denied (publickey,password)".
func isAuthError(err error) bool {
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

// Dial reports whether a TCP connection to addr can be made.
func Dial(ctx context.Context, addr string) error {
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	return conn.Close()
}
