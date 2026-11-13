package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseFlags(t *testing.T) {
	var buf bytes.Buffer
	o, err := parseFlags([]string{"--target", "192.0.2.10", "--host", "myhost", "--verbose"}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if o.Target != "192.0.2.10" || o.Host != "myhost" || o.ConfigDir != "" || !o.verbose || o.Reinstall || o.GenerateHardware {
		t.Fatalf("got %+v", o)
	}
	if o, err := parseFlags([]string{"--secrets", "../nix-secrets"}, &buf); err != nil || o.SecretsDir != "../nix-secrets" {
		t.Fatalf("--secrets: %+v %v", o, err)
	}
	if o, err := parseFlags([]string{"--generate-hardware"}, &buf); err != nil || !o.GenerateHardware {
		t.Fatalf("--generate-hardware: %+v %v", o, err)
	}
	if o, err := parseFlags([]string{"--reinstall"}, &buf); err != nil || !o.Reinstall {
		t.Fatalf("--reinstall: %+v %v", o, err)
	}
	empty, err := parseFlags(nil, &buf)
	if err != nil || empty.Target != "" {
		t.Fatalf("empty target should parse: %+v %v", empty, err)
	}
	for _, bad := range []string{"nope", "127.0.0.1", "::1", "0.0.0.0", "224.0.0.1"} {
		if _, err := parseFlags([]string{"--target", bad}, &buf); err == nil {
			t.Fatalf("expected invalid target error for %q", bad)
		}
	}
	for _, good := range []string{"2001:db8::5", "fe80::1%eth0"} {
		if _, err := parseFlags([]string{"--target", good}, &buf); err != nil {
			t.Fatalf("IPv6 target %q: %v", good, err)
		}
	}
	if _, err := parseFlags([]string{"--target", "fe80::1"}, &buf); err == nil || !strings.Contains(err.Error(), "zone") {
		t.Fatalf("link-local without zone should ask for a zone, got %v", err)
	}
}

func TestSSHPasswordFlag(t *testing.T) {
	var buf bytes.Buffer
	if o, err := parseFlags([]string{"--ssh-password", "x"}, &buf); err != nil || o.SSHPassword != "x" {
		t.Errorf("password = %q, %v", o.SSHPassword, err)
	}
}

func TestRekeyViaFlag(t *testing.T) {
	var buf bytes.Buffer
	o, err := parseFlags([]string{"--rekey-via", "atlas=192.0.2.20", "--rekey-via", "broadway=broadway.lan"}, &buf)
	if err != nil || o.RekeyVia["atlas"] != "192.0.2.20" || o.RekeyVia["broadway"] != "broadway.lan" || len(o.RekeyVia) != 2 {
		t.Fatalf("rekey-via = %v, %v", o.RekeyVia, err)
	}
	for _, bad := range []string{"atlas", "=192.0.2.20", "atlas="} {
		if _, err := parseFlags([]string{"--rekey-via", bad}, &buf); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}
