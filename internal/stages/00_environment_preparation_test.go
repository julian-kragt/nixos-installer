package stages

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
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

func TestDialTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := dialTCP(context.Background(), addr); err != nil {
		t.Errorf("listening port: %v", err)
	}
	ln.Close()
	if err := dialTCP(context.Background(), addr); err == nil {
		t.Error("closed port: want an error")
	}
}

func TestCheckConfig(t *testing.T) {
	dir := t.TempDir()
	if err := checkConfig(dir); err == nil {
		t.Error("want an error without flake.nix")
	}
	if err := os.WriteFile(filepath.Join(dir, "flake.nix"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkConfig(dir); err != nil {
		t.Error(err)
	}
}
