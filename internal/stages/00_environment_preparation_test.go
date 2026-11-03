package stages

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
)

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
