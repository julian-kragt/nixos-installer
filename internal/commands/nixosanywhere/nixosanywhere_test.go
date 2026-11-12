package nixosanywhere

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/julian-kragt/nixos-installer/internal/machine"
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
		{"iso named like the host", "myhost", isoRelease, TargetISO},
		{"installed", "myhost", installedRelease, TargetInstalled},
		{"another nixos host", "nexthost", installedRelease, TargetOther},
		{"another distribution", "myhost", "ID=debian\n", TargetOther},
		{"variant of another distribution", "myhost", "ID=fedora\nVARIANT_ID=installer\n", TargetOther},
	}
	for _, c := range cases {
		if got := classifyTarget(c.hostname, c.osRelease, "myhost"); got != c.want {
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

	kind, err := Inspect(context.Background(), machine.Loopback(), "myhost")
	if err != nil || kind != TargetISO {
		t.Fatalf("got %v, %v", kind, err)
	}

	osReleasePath = filepath.Join(t.TempDir(), "missing")
	if _, err := Inspect(context.Background(), machine.Loopback(), "myhost"); err == nil {
		t.Error("unreadable os-release: want an error, not a guess")
	}
}

func TestInstallArgs(t *testing.T) {
	want := []string{"run", ref, "--",
		"--flake", "git+file:///c#myhost-bootstrap", "--target-host", "root@192.0.2.1", "--build-on", "remote",
		"--generate-hardware-config", "nixos-generate-config", "hosts/nixos/myhost/hw.nix"}
	if got := installArgs("git+file:///c", "myhost", "192.0.2.1", "hosts/nixos/myhost/hw.nix"); !slices.Equal(got, want) {
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
