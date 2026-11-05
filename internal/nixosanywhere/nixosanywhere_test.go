package nixosanywhere

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"installer/internal/machine"
)

const (
	isoRelease       = "ID=nixos\nVARIANT_ID=installer\nNAME=NixOS\n"
	installedRelease = "ID=nixos\nNAME=NixOS\n"
)

func TestClassifyTarget(t *testing.T) {
	cases := []struct {
		name, hostname, osRelease string
		want                      TargetKind
	}{
		{"iso", "nixos", isoRelease, TargetISO},
		{"iso with quotes", "nixos", "ID=\"nixos\"\nVARIANT_ID=\"installer\"\n", TargetISO},
		{"iso named like the host", "atlas", isoRelease, TargetISO},
		{"installed", "atlas", installedRelease, TargetInstalled},
		{"another nixos host", "broadway", installedRelease, TargetOther},
		{"another distribution", "atlas", "ID=debian\n", TargetOther},
		{"variant of another distribution", "atlas", "ID=fedora\nVARIANT_ID=installer\n", TargetOther},
	}
	for _, c := range cases {
		if got := classifyTarget(c.hostname, c.osRelease, "atlas"); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestInspect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "os-release")
	if err := os.WriteFile(path, []byte(isoRelease), 0o600); err != nil {
		t.Fatal(err)
	}
	old := osReleasePath
	osReleasePath = path
	defer func() { osReleasePath = old }()

	kind, err := Inspect(context.Background(), machine.Loopback(), "atlas")
	if err != nil || kind != TargetISO {
		t.Fatalf("got %v, %v", kind, err)
	}

	osReleasePath = filepath.Join(t.TempDir(), "missing")
	if _, err := Inspect(context.Background(), machine.Loopback(), "atlas"); err == nil {
		t.Error("unreadable os-release: want an error, not a guess")
	}
}

func TestInstallArgs(t *testing.T) {
	want := []string{"run", ref, "--",
		"--flake", "git+file:///c#atlas-bootstrap", "--target-host", "root@192.0.2.1", "--build-on", "remote",
		"--generate-hardware-config", "nixos-generate-config", "hosts/nixos/atlas/hw.nix"}
	if got := installArgs("git+file:///c", "atlas", "192.0.2.1", "hosts/nixos/atlas/hw.nix"); !slices.Equal(got, want) {
		t.Errorf("got %q", got)
	}
}

func TestConfigFlakeURL(t *testing.T) {
	if got := configFlakeURL("/home/me/config", ""); got != "git+file:///home/me/config" {
		t.Errorf("at the top: %s", got)
	}
	if got := configFlakeURL("/home/me/repo/sub/config", "sub/config/"); got != "git+file:///home/me/repo?dir=sub/config" {
		t.Errorf("below the top: %s", got)
	}
}
