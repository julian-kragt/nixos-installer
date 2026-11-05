package machine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/ssh"
)

// HostPin is the one SSH host key trusted for a host. It is keyed by host name,
// not by address, because the target IP is a per-run input. The ISO's key is
// pinned at first contact and the installed system's key replaces it.
type HostPin struct {
	Host string // the name the key is filed under (ssh's HostKeyAlias)
	File string // a known_hosts file holding only this host
}

// NewHostPin returns the pin of host, kept next to its state file.
func NewHostPin(stateDir, host string) HostPin {
	return HostPin{Host: host, File: filepath.Join(stateDir, host+".known_hosts")}
}

// Load returns the pinned key, or nil when nothing is pinned yet.
func (p HostPin) Load() (ssh.PublicKey, error) {
	data, err := os.ReadFile(p.File)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("host key: %w", err)
	}
	_, _, key, _, _, err := ssh.ParseKnownHosts(data)
	if err != nil {
		return nil, fmt.Errorf("host key: parse %s: %w", p.File, err)
	}
	return key, nil
}

// Save pins key in place of whatever was pinned, atomically.
func (p HostPin) Save(key ssh.PublicKey) error {
	if err := os.MkdirAll(filepath.Dir(p.File), 0o700); err != nil {
		return fmt.Errorf("host key: create dir: %w", err)
	}
	line := p.Host + " " + string(ssh.MarshalAuthorizedKey(key))
	tmp := p.File + ".tmp"
	if err := os.WriteFile(tmp, []byte(line), 0o600); err != nil {
		return fmt.Errorf("host key: write: %w", err)
	}
	if err := os.Rename(tmp, p.File); err != nil {
		return fmt.Errorf("host key: rename: %w", err)
	}
	return nil
}

// SameKey reports whether a and b are the same key.
func SameKey(a, b ssh.PublicKey) bool {
	return a != nil && b != nil && bytes.Equal(a.Marshal(), b.Marshal())
}

// Fingerprint is key as ssh shows it: SHA256:...
func Fingerprint(key ssh.PublicKey) string { return ssh.FingerprintSHA256(key) }

// ScanHostKey returns the ed25519 host key offered at addr:22, without logging in.
func ScanHostKey(ctx context.Context, addr string) (ssh.PublicKey, error) {
	return scanHostKey(ctx, net.JoinHostPort(addr, "22"))
}

// hostKeyPollInterval is a variable so tests can change it.
var hostKeyPollInterval = 5 * time.Second

// WaitForNewHostKey polls addr until it offers a key other than old, which is the
// ISO's: until then the machine has not rebooted, or is at the passphrase prompt
// with no ssh at all.
func WaitForNewHostKey(ctx context.Context, addr string, old ssh.PublicKey, timeout time.Duration) (ssh.PublicKey, error) {
	return waitForNewHostKey(ctx, old, timeout, func(ctx context.Context) (ssh.PublicKey, error) {
		return ScanHostKey(ctx, addr)
	})
}

func waitForNewHostKey(ctx context.Context, old ssh.PublicKey, timeout time.Duration, scan func(context.Context) (ssh.PublicKey, error)) (ssh.PublicKey, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, timeout,
		fmt.Errorf("the installed system did not come up within %s; run the installer again once it has", timeout))
	defer cancel()
	for {
		if key, err := scan(ctx); err == nil && !SameKey(key, old) {
			return key, nil
		}
		select {
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		case <-time.After(hostKeyPollInterval):
		}
	}
}

func scanHostKey(ctx context.Context, hostPort string) (ssh.PublicKey, error) {
	const timeout = 10 * time.Second
	conn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", hostPort)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	// Closing conn interrupts ssh.NewClientConn, which takes no ctx.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}

	var offered ssh.PublicKey
	errScanned := errors.New("host key read")
	_, _, _, err = ssh.NewClientConn(conn, hostPort, &ssh.ClientConfig{
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			offered = key
			return errScanned // the key is all that is needed: end the handshake
		},
	})
	if offered == nil {
		return nil, err
	}
	return offered, nil
}
