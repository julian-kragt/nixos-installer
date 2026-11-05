package stages

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"installer/internal/pipeline"
	"installer/internal/runner"
)

func newKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

const (
	isoRelease       = "ID=nixos\nVARIANT_ID=installer\nNAME=NixOS\n"
	installedRelease = "ID=nixos\nNAME=NixOS\n"
)

func TestClassifyTarget(t *testing.T) {
	cases := []struct {
		name, hostname, osRelease string
		want                      targetKind
	}{
		{"iso", "nixos", isoRelease, targetISO},
		{"iso with quotes", "nixos", "ID=\"nixos\"\nVARIANT_ID=\"installer\"\n", targetISO},
		{"iso named like the host", "atlas", isoRelease, targetISO},
		{"installed", "atlas", installedRelease, targetInstalled},
		{"another nixos host", "broadway", installedRelease, targetOther},
		{"another distribution", "atlas", "ID=debian\n", targetOther},
		{"variant of another distribution", "atlas", "ID=fedora\nVARIANT_ID=installer\n", targetOther},
	}
	for _, c := range cases {
		if got := classifyTarget(c.hostname, c.osRelease, "atlas"); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestInspectTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "os-release")
	if err := os.WriteFile(path, []byte(isoRelease), 0o600); err != nil {
		t.Fatal(err)
	}
	old := osReleasePath
	osReleasePath = path
	defer func() { osReleasePath = old }()

	kind, err := inspectTarget(context.Background(), pipeline.Loopback(), "atlas")
	if err != nil || kind != targetISO {
		t.Fatalf("got %v, %v", kind, err)
	}

	osReleasePath = filepath.Join(t.TempDir(), "missing")
	if _, err := inspectTarget(context.Background(), pipeline.Loopback(), "atlas"); err == nil {
		t.Error("unreadable os-release: want an error, not a guess")
	}
}

func TestEnsurePassphrase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disko-password")
	old := diskPasswordPath
	diskPasswordPath = path
	defer func() { diskPasswordPath = old }()

	// Gone, as after a reboot of the ISO: asked again.
	ui := runner.New(runner.Options{In: strings.NewReader("s3cret\ns3cret\n")})
	if err := ensurePassphrase(runner.NewContext(context.Background(), ui), pipeline.Loopback()); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "s3cret" {
		t.Fatalf("file = %q", data)
	}

	// Still there: nothing is asked, and there is nobody to answer.
	ui = runner.New(runner.Options{In: strings.NewReader("")})
	if err := ensurePassphrase(runner.NewContext(context.Background(), ui), pipeline.Loopback()); err != nil {
		t.Errorf("present: %v", err)
	}
}

func TestNixosAnywhereArgs(t *testing.T) {
	want := []string{"run", nixosAnywhereRef, "--",
		"--flake", "git+file:///c#atlas-bootstrap", "--target-host", "root@192.0.2.1", "--build-on", "remote",
		"--generate-hardware-config", "nixos-generate-config", "hosts/nixos/atlas/hw.nix"}
	if got := nixosAnywhereArgs("git+file:///c", "atlas", "192.0.2.1", "hosts/nixos/atlas/hw.nix"); !slices.Equal(got, want) {
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

func TestWaitForNewHostKey(t *testing.T) {
	old := hostKeyPollInterval
	hostKeyPollInterval = time.Millisecond
	defer func() { hostKeyPollInterval = old }()

	iso, installed := newKey(t), newKey(t)
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
	if err != nil || !pipeline.SameKey(got, installed) || calls != 3 {
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
	pinned, other := newKey(t), newKey(t)
	cases := []struct {
		name      string
		pin       ssh.PublicKey
		offered   ssh.PublicKey
		completed []string
		answer    string
		wantErr   string
		wantPin   ssh.PublicKey
	}{
		{name: "first contact pins", offered: other, wantPin: other},
		{name: "same key", pin: pinned, offered: pinned, wantPin: pinned},
		{name: "changed and trusted", pin: pinned, offered: other, answer: "trust\n", wantPin: other},
		{name: "changed and declined", pin: pinned, offered: other, answer: "no\n", wantErr: "aborted", wantPin: pinned},
		{name: "changed, nobody to ask", pin: pinned, offered: other, wantErr: "host key", wantPin: pinned},
		{name: "changed after the install", pin: pinned, offered: other, completed: []string{"0.2"}, answer: "trust\n", wantErr: "not the system", wantPin: pinned},
	}
	for _, c := range cases {
		env := newEnv(t, "", "")
		env.Pin = pipeline.NewHostPin(t.TempDir(), "h")
		env.State.Completed = c.completed
		if c.pin != nil {
			if err := env.Pin.Save(c.pin); err != nil {
				t.Fatal(err)
			}
		}
		ui := runner.New(runner.Options{In: strings.NewReader(c.answer)})
		err := checkHostKey(runner.NewContext(context.Background(), ui), env, c.offered)
		if c.wantErr == "" && err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.wantErr)
		}
		if got, _ := env.Pin.Load(); !pipeline.SameKey(got, c.wantPin) {
			t.Errorf("%s: wrong key pinned", c.name)
		}
	}
}
