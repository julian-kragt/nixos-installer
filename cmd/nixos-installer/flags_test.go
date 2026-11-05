package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseFlags(t *testing.T) {
	var buf bytes.Buffer
	o, err := parseFlags([]string{"--target", "192.168.1.100", "--host", "atlas", "--verbose"}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if o.Target != "192.168.1.100" || o.Host != "atlas" || o.ConfigDir != "" || o.Disk != "" || !o.verbose {
		t.Fatalf("got %+v", o)
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
