package pipeline

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// ErrAuth is what InstallAuthorizedKeys returns when the target rejects the password.
var ErrAuth = errors.New("password authentication failed")

// authError wraps err in ErrAuth when it is the ssh package's authentication
// failure, which has no type of its own.
func authError(err error) error {
	if strings.Contains(err.Error(), "unable to authenticate") {
		return fmt.Errorf("%w: %w", ErrAuth, err)
	}
	return err
}

// InstallAuthorizedKeys connects to addr:22 as root with password, installs
// authorizedKeys into /root/.ssh/authorized_keys, and disconnects.
// It is used only once, before key auth is set up. The password is only sent
// to the system holding the key in pin.
func InstallAuthorizedKeys(ctx context.Context, addr, password, authorizedKeys string, pin HostPin) error {
	return installAuthorizedKeys(ctx, net.JoinHostPort(addr, "22"), password, authorizedKeys, pin)
}

func installAuthorizedKeys(ctx context.Context, addr, password, authorizedKeys string, pin HostPin) error {
	pinned, err := pin.Load()
	if err != nil {
		return err
	}
	if pinned == nil {
		return errors.New("no host key pinned for " + pin.Host)
	}
	cfg := &ssh.ClientConfig{
		User:            "root",
		Auth:            []ssh.AuthMethod{ssh.Password(password)},
		HostKeyCallback: ssh.FixedHostKey(pinned),
		Timeout:         10 * time.Second,
	}

	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	// Closing conn interrupts ssh.NewClientConn and sess.Run, which take no ctx.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		return authError(err)
	}
	client := ssh.NewClient(c, chans, reqs)
	defer client.Close()

	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	sess.Stdin = strings.NewReader(authorizedKeys)
	return sess.Run(
		"mkdir -p /root/.ssh && chmod 700 /root/.ssh && " +
			"touch /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys && " +
			"cat - >> /root/.ssh/authorized_keys && " +
			"sort -u -o /root/.ssh/authorized_keys /root/.ssh/authorized_keys",
	)
}
