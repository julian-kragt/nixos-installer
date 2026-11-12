package machine

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/julian-kragt/nixos-installer/internal/runner"
)

func TestOffersPassword(t *testing.T) {
	cases := []struct {
		name string
		err  string
		want bool
	}{
		{"installed system", "true: exit status 255: root@10.0.0.1: Permission denied (publickey).", false},
		{"ISO", "true: exit status 255: root@10.0.0.1: Permission denied (publickey,password,keyboard-interactive).", true},
		{"keyboard-interactive is not password", "true: exit status 255: root@10.0.0.1: Permission denied (publickey,keyboard-interactive).", false},
		{"not an auth failure", "true: exit status 255: ssh: connect to host 10.0.0.1 port 22: password", false},
	}
	for _, c := range cases {
		if got := offersPassword(errors.New(c.err)); got != c.want {
			t.Errorf("%s: offersPassword = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDial(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := Dial(context.Background(), addr); err != nil {
		t.Errorf("listening port: %v", err)
	}
	ln.Close()
	if err := Dial(context.Background(), addr); err == nil {
		t.Error("closed port: want an error")
	}
}

func TestKeyAuthorized(t *testing.T) {
	agent := "ssh-ed25519 AAAAone me@laptop\nssh-ed25519 AAAAtwo other\n"
	cases := []struct {
		name       string
		agent      string
		authorized []string
		want       bool
	}{
		{"match", agent, []string{"ssh-ed25519 AAAAtwo some-comment"}, true},
		{"match with options", agent, []string{`command="x" ssh-ed25519 AAAAone`}, true},
		{"no match", agent, []string{"ssh-ed25519 AAAAthree"}, false},
		{"empty agent", "The agent has no identities.\n", []string{"ssh-ed25519 AAAAone"}, false},
		{"nothing authorized", agent, nil, false},
	}
	for _, c := range cases {
		if got := KeyAuthorized(c.agent, c.authorized); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func TestWaitForNewHostKey(t *testing.T) {
	old := hostKeyPollInterval
	hostKeyPollInterval = time.Millisecond
	defer func() { hostKeyPollInterval = old }()

	iso, installed := newSigner(t).PublicKey(), newSigner(t).PublicKey()
	ctx := context.Background()

	// Still the ISO, then no ssh while at the passphrase prompt, then the new system.
	replies := []struct {
		key ssh.PublicKey
		err error
	}{{iso, nil}, {nil, errors.New("connection refused")}, {installed, nil}}
	calls := 0
	got, err := waitForNewHostKey(ctx, iso, time.Minute, func(context.Context) (ssh.PublicKey, error) {
		r := replies[calls]
		calls++
		return r.key, r.err
	})
	if err != nil || !SameKey(got, installed) || calls != 3 {
		t.Errorf("got %v, %v after %d calls", got, err, calls)
	}

	// The ISO's key never counts as the installed system.
	_, err = waitForNewHostKey(ctx, iso, 20*time.Millisecond, func(context.Context) (ssh.PublicKey, error) {
		return iso, nil
	})
	if err == nil || !strings.Contains(err.Error(), "did not come up") {
		t.Errorf("timeout: err = %v", err)
	}
}

func TestCheckHostKey(t *testing.T) {
	pinned, other := newSigner(t).PublicKey(), newSigner(t).PublicKey()
	cases := []struct {
		name      string
		pin       ssh.PublicKey
		offered   ssh.PublicKey
		installed bool
		answer    string
		wantErr   string
		wantPin   ssh.PublicKey
	}{
		{name: "first contact pins", offered: other, wantPin: other},
		{name: "same key", pin: pinned, offered: pinned, wantPin: pinned},
		{name: "changed and trusted", pin: pinned, offered: other, answer: "trust\n", wantPin: other},
		{name: "changed and declined", pin: pinned, offered: other, answer: "no\n", wantErr: "aborted", wantPin: pinned},
		{name: "changed, nobody to ask", pin: pinned, offered: other, wantErr: "host key", wantPin: pinned},
		{name: "changed after the install", pin: pinned, offered: other, installed: true, answer: "trust\n", wantErr: "--reinstall", wantPin: pinned},
	}
	for _, c := range cases {
		a := Access{Addr: "192.0.2.1", Pin: NewHostPin(t.TempDir(), "h"), Installed: c.installed}
		if c.pin != nil {
			if err := a.Pin.Save(c.pin); err != nil {
				t.Fatal(err)
			}
		}
		ui := runner.New(runner.Options{In: strings.NewReader(c.answer)})
		err := a.checkOffered(runner.NewContext(context.Background(), ui), c.offered)
		if c.wantErr == "" && err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.wantErr)
		}
		if got, _ := a.Pin.Load(); !SameKey(got, c.wantPin) {
			t.Errorf("%s: wrong key pinned", c.name)
		}
	}
}

func TestAgentKeys(t *testing.T) {
	for _, c := range []struct {
		name, script string
		wantErr      bool
	}{
		{"keys", "echo 'ssh-ed25519 AAAA me'", false},
		{"empty agent exits 1", "echo 'The agent has no identities.'; exit 1", true},
		{"empty agent exits 0", "echo 'The agent has no identities.'", true},
		{"no output", "true", true},
	} {
		bin := t.TempDir()
		if err := os.WriteFile(filepath.Join(bin, "ssh-add"), []byte("#!/bin/sh\n"+c.script+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
		got, err := Local().AgentKeys(context.Background())
		if (err != nil) != c.wantErr {
			t.Errorf("%s: got %q, %v", c.name, got, err)
		}
	}
}

func TestForwardedAgents(t *testing.T) {
	// Short: macOS allows 104 bytes for a socket path, and t.TempDir is long.
	dir, err := os.MkdirTemp("/tmp", "fa")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	for _, name := range []string{"agent.live", "agent.stale"} {
		l, err := net.Listen("unix", filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Close() })
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	for name, script := range map[string]string{
		"ssh-add": `[ "$1" = -l ] || exit 9; case $SSH_AUTH_SOCK in */agent.live) exit 0 ;; esac; exit 2`,
		"timeout": `shift; exec "$@"`,
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	old := agentSockets
	agentSockets = []string{filepath.Join(dir, "agent.*"), filepath.Join(dir, "missing-*/agent.*")}
	t.Cleanup(func() { agentSockets = old })

	got, err := Loopback().ForwardedAgents(context.Background())
	if want := filepath.Join(dir, "agent.live"); err != nil || len(got) != 1 || got[0] != want {
		t.Fatalf("got %q, %v; want only %s", got, err, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "agent.stale")); !os.IsNotExist(err) {
		t.Errorf("stale socket not removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "agent.file")); err != nil {
		t.Errorf("plain file touched: %v", err)
	}
}
