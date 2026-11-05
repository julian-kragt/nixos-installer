package nix

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"installer/internal/machine"
)

// fakeNix puts a `nix` on PATH that prints a warning on stderr and body on
// stdout. It returns a function giving the args of the last call.
func fakeNix(t *testing.T, body string) func() []string {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argsFile + "'\necho 'warning: dirty tree' >&2\nprintf '%s' '" + body + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "nix"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return func() []string {
		data, err := os.ReadFile(argsFile)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(strings.TrimSpace(string(data)), "\n")
	}
}

const config = "/home/me/config"

var cfg = Config{machine.Local(), config}

// evalArgs is the command line every eval of config runs, before extra.
func evalArgs(attr string, extra ...string) []string {
	return append([]string{"eval", "--json", config + "#" + attr,
		"--no-write-lock-file"}, extra...)
}

func TestEvalJSONIgnoresStderr(t *testing.T) {
	fakeNix(t, `{"disk":"/dev/vda"}`)
	var got struct{ Disk string }
	if err := evalJSON(context.Background(), machine.Local(), ".#x", &got); err != nil {
		t.Fatal(err)
	}
	if got.Disk != "/dev/vda" {
		t.Fatalf("got %+v", got)
	}
}

func TestEvalJSONRejectsGarbage(t *testing.T) {
	fakeNix(t, `not json`)
	var got any
	if err := evalJSON(context.Background(), machine.Local(), ".#x", &got); err == nil {
		t.Fatal("want a decode error")
	}
}

func TestHardwareConfigPath(t *testing.T) {
	args := fakeNix(t, `"hosts/a/hw.nix"`)
	got, err := cfg.HardwareConfigPath(context.Background(), "atlas")
	if err != nil {
		t.Fatal(err)
	}
	if want := evalArgs("bootstrapEntries.atlas.hardwareConfigPath"); !slices.Equal(args(), want) {
		t.Errorf("args = %q, want %q", args(), want)
	}
	if got != "hosts/a/hw.nix" {
		t.Errorf("got %q", got)
	}
}

func TestHostInfo(t *testing.T) {
	args := fakeNix(t, `{"hardwareConfigPath":"hosts/a/hw.nix","rootAuthorizedKeys":["ssh-ed25519 AAAA me"],"system":"x86_64-linux","disks":{"main":"/dev/vda"},`+
		`"users":{"auto":{"home":"/home/auto","uid":null},"fixed":{"home":"/data/fixed","uid":1500}}}`)
	got, err := cfg.HostInfo(context.Background(), "atlas")
	if err != nil {
		t.Fatal(err)
	}
	if want := evalArgs("installerData.atlas"); !slices.Equal(args(), want) {
		t.Errorf("args = %q, want %q", args(), want)
	}
	if got.System != "x86_64-linux" || got.HardwareConfigPath != "hosts/a/hw.nix" || len(got.RootAuthorizedKeys) != 1 || got.Disks["main"] != "/dev/vda" {
		t.Errorf("got %+v", got)
	}
	if u := got.Users["auto"]; u.UID != nil || u.Home != "/home/auto" {
		t.Errorf("auto = %+v", u)
	}
	if u := got.Users["fixed"]; u.UID == nil || *u.UID != 1500 || u.Home != "/data/fixed" {
		t.Errorf("fixed = %+v", u)
	}
}

func TestHosts(t *testing.T) {
	args := fakeNix(t, `["atlas","broadway"]`)
	got, err := cfg.Hosts(context.Background())
	if err != nil || !slices.Equal(got, []string{"atlas", "broadway"}) {
		t.Fatalf("got %q, %v", got, err)
	}
	if want := evalArgs("installerData", "--apply", "builtins.attrNames"); !slices.Equal(args(), want) {
		t.Errorf("args = %q, want %q", args(), want)
	}
}

func TestCheckBootstrapSystem(t *testing.T) {
	args := fakeNix(t, `"/nix/store/x.drv"`)
	if err := cfg.CheckBootstrapSystem(context.Background(), "atlas"); err != nil {
		t.Fatal(err)
	}
	if want := evalArgs("nixosConfigurations.atlas-bootstrap.config.system.build.toplevel.drvPath"); !slices.Equal(args(), want) {
		t.Errorf("args = %q, want %q", args(), want)
	}
}

func TestFeaturesWithoutNixCommand(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\necho \"error: experimental Nix feature 'nix-command' is disabled; add '--extra-experimental-features nix-command' to enable it\" >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "nix"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	got, err := Features(context.Background(), machine.Loopback())
	if err != nil || len(got) != 0 {
		t.Errorf("features = %q, %v; want none and no error", got, err)
	}
}

func TestFeatures(t *testing.T) {
	tests := []struct {
		name, out string
		want      []string
	}{
		{"one line", "nix-command flakes pipe-operators\n", []string{"nix-command", "flakes", "pipe-operators"}},
		{"none", "\n", nil},
		{"extra spaces", "  flakes   nix-command", []string{"flakes", "nix-command"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := fakeNix(t, tt.out)
			got, err := Features(context.Background(), machine.Loopback())
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("features = %q, want %q", got, tt.want)
			}
			if want := []string{"config", "show", "experimental-features"}; !slices.Equal(args(), want) {
				t.Errorf("args = %q, want %q", args(), want)
			}
		})
	}
}
