package machine

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"
)

func newSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

// startSSHServer serves cfg on a free local port until the test ends. Every
// exec request succeeds, and what it reads on stdin goes to received.
func startSSHServer(t *testing.T, cfg *ssh.ServerConfig, received chan<- string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSSH(conn, cfg, received)
		}
	}()
	return ln.Addr().String()
}

func serveSSH(conn net.Conn, cfg *ssh.ServerConfig, received chan<- string) {
	defer conn.Close()
	server, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer server.Close()
	go ssh.DiscardRequests(reqs)
	for newChan := range chans {
		ch, requests, err := newChan.Accept()
		if err != nil {
			return
		}
		go func() {
			for req := range requests {
				if req.Type != "exec" {
					req.Reply(false, nil)
					continue
				}
				req.Reply(true, nil)
				stdin, _ := io.ReadAll(ch)
				if received != nil {
					received <- string(stdin)
				}
				ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
				ch.Close()
			}
		}()
	}
}

func openServer(signer ssh.Signer) *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	return cfg
}

func TestHostPinLoadAndSave(t *testing.T) {
	pin := NewHostPin(filepath.Join(t.TempDir(), "new"), "atlas")
	if key, err := pin.Load(); err != nil || key != nil {
		t.Fatalf("nothing pinned: key = %v, err = %v", key, err)
	}

	first, second := newSigner(t).PublicKey(), newSigner(t).PublicKey()
	for _, key := range []ssh.PublicKey{first, second} { // the second replaces the first
		if err := pin.Save(key); err != nil {
			t.Fatal(err)
		}
		got, err := pin.Load()
		if err != nil || !SameKey(got, key) {
			t.Fatalf("got %v, %v", got, err)
		}
	}

	data, err := os.ReadFile(pin.File)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(data), "\n"); lines != 1 || !strings.HasPrefix(string(data), "atlas ssh-ed25519 ") {
		t.Errorf("known_hosts = %q, want one line filed under the host name", data)
	}
	if fi, _ := os.Stat(pin.File); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode())
	}
}

func TestSameKey(t *testing.T) {
	a, b := newSigner(t).PublicKey(), newSigner(t).PublicKey()
	if !SameKey(a, a) || SameKey(a, b) || SameKey(a, nil) || SameKey(nil, nil) {
		t.Error("SameKey must hold only for two equal keys")
	}
}

func TestScanHostKey(t *testing.T) {
	signer := newSigner(t)
	addr := startSSHServer(t, openServer(signer), nil)
	got, err := scanHostKey(context.Background(), addr)
	if err != nil || !SameKey(got, signer.PublicKey()) {
		t.Fatalf("got %v, %v", got, err)
	}
}

// sshTo is SSH with the port of a test server and without the user's ssh config.
func sshTo(t *testing.T, addr string, pin HostPin) Machine {
	t.Helper()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	m := SSH(host, pin)
	m.ssh = slices.Insert(m.ssh, 1, "-F", "/dev/null", "-p", port)
	return m
}

func TestSSHChecksThePin(t *testing.T) {
	signer := newSigner(t)
	addr := startSSHServer(t, openServer(signer), nil)
	pin := NewHostPin(filepath.Join(t.TempDir(), "with space"), "atlas")

	if err := pin.Save(signer.PublicKey()); err != nil {
		t.Fatal(err)
	}
	if _, err := sshTo(t, addr, pin).Run(context.Background(), "true"); err != nil {
		t.Errorf("pinned key: %v", err)
	}

	if err := pin.Save(newSigner(t).PublicKey()); err != nil {
		t.Fatal(err)
	}
	_, err := sshTo(t, addr, pin).Run(context.Background(), "true")
	if err == nil || !strings.Contains(err.Error(), "Host key verification failed") {
		t.Errorf("changed key: err = %v, want it refused", err)
	}
}

func TestInstallAuthorizedKeysChecksThePinBeforeThePassword(t *testing.T) {
	signer := newSigner(t)
	var passwords atomic.Int32
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			passwords.Add(1)
			return nil, nil
		},
	}
	cfg.AddHostKey(signer)
	received := make(chan string, 1)
	addr := startSSHServer(t, cfg, received)
	ctx := context.Background()
	pin := NewHostPin(t.TempDir(), "atlas")

	if err := installAuthorizedKeys(ctx, addr, "pw", "ssh-ed25519 AAAA\n", pin); err == nil {
		t.Error("nothing pinned: want an error")
	}

	if err := pin.Save(newSigner(t).PublicKey()); err != nil {
		t.Fatal(err)
	}
	if err := installAuthorizedKeys(ctx, addr, "pw", "ssh-ed25519 AAAA\n", pin); err == nil {
		t.Error("changed key: want an error")
	}
	if n := passwords.Load(); n != 0 {
		t.Errorf("the password was offered %d times to the wrong system", n)
	}

	if err := pin.Save(signer.PublicKey()); err != nil {
		t.Fatal(err)
	}
	if err := installAuthorizedKeys(ctx, addr, "pw", "ssh-ed25519 AAAA\n", pin); err != nil {
		t.Fatalf("pinned key: %v", err)
	}
	if got := <-received; got != "ssh-ed25519 AAAA\n" {
		t.Errorf("keys sent = %q", got)
	}
}

// A NixOS ISO also has an RSA host key, which ssh's default algorithm order
// prefers to the ed25519 key that is pinned.
func TestInstallAuthorizedKeysAsksForThePinnedKeyType(t *testing.T) {
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaSigner, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	signer := newSigner(t)
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { return nil, nil },
	}
	cfg.AddHostKey(rsaSigner)
	cfg.AddHostKey(signer)
	received := make(chan string, 1)
	addr := startSSHServer(t, cfg, received)
	pin := NewHostPin(t.TempDir(), "atlas")
	if err := pin.Save(signer.PublicKey()); err != nil {
		t.Fatal(err)
	}

	if err := installAuthorizedKeys(context.Background(), addr, "pw", "ssh-ed25519 AAAA\n", pin); err != nil {
		t.Fatalf("pinned key: %v", err)
	}
	if got := <-received; got != "ssh-ed25519 AAAA\n" {
		t.Errorf("keys sent = %q", got)
	}
}
