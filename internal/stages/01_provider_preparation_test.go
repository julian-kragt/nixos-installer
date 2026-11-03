package stages

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"installer/internal/pipeline"
	"installer/internal/runner"
)

func newEnv(t *testing.T, disk, recorded string) *pipeline.Env {
	t.Helper()
	st, err := pipeline.LoadState(t.TempDir(), "h")
	if err != nil {
		t.Fatal(err)
	}
	st.Disk = recorded
	return &pipeline.Env{Inputs: pipeline.Inputs{Host: "h", Disk: disk}, State: st, Remote: pipeline.Loopback()}
}

func TestResolveDiskPrefersTheFlag(t *testing.T) {
	// A flag short-circuits listing: lsblk does not exist on every machine.
	got, err := resolveDisk(context.Background(), newEnv(t, "/dev/flag", ""))
	if err != nil || got != "/dev/flag" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestResolveDiskGuardsTheRecordedDisk(t *testing.T) {
	dir := t.TempDir()
	real, link := filepath.Join(dir, "sda"), filepath.Join(dir, "by-id")
	if err := os.WriteFile(real, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(real)

	// The flag names the recorded disk through a symlink: the same disk.
	got, err := resolveDisk(context.Background(), newEnv(t, link, resolved))
	if err != nil || got != resolved {
		t.Errorf("same disk: got %q, %v", got, err)
	}
	// Another disk than the recorded one is refused.
	_, err = resolveDisk(context.Background(), newEnv(t, filepath.Join(dir, "other"), resolved))
	if err == nil || !strings.Contains(err.Error(), "was used before") {
		t.Errorf("other disk: err = %v", err)
	}
}

func TestAgentKeyAuthorized(t *testing.T) {
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
		if got := agentKeyAuthorized(c.agent, c.authorized); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func TestUnmanagedChanges(t *testing.T) {
	dirty := []string{
		"cfg/flake.lock",
		"cfg/machines/h/hw.nix",
		"cfg/hosts/nixos/h/hardware-configuration.nix",
		"flake.lock",
	}
	want := []string{"cfg/hosts/nixos/h/hardware-configuration.nix", "flake.lock"}
	if got := unmanagedChanges(dirty, "cfg/", "machines/h/hw.nix"); !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := unmanagedChanges(dirty[:2], "cfg/", "machines/h/hw.nix"); len(got) != 0 {
		t.Errorf("managed only: got %q", got)
	}
}

func TestCheckConfigDisk(t *testing.T) {
	dir := t.TempDir()
	file, link := filepath.Join(dir, "sda"), filepath.Join(dir, "by-id")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		devices map[string]string
		wantErr string
	}{
		{"no disks", nil, "set the device"},
		{"other disk", map[string]string{"main": filepath.Join(dir, "other")}, "set the device"},
		{"device directory missing", map[string]string{"main": "/dev/disk/nope/REPLACE_ME"}, "set the device"},
		// A plain file stands in for the disk: found, then refused as no block device.
		{"found through a symlink", map[string]string{"main": link}, "not a block device"},
		{"second disk found", map[string]string{"a": filepath.Join(dir, "other"), "b": file}, "not a block device"},
	}
	for _, c := range cases {
		err := checkConfigDisk(context.Background(), pipeline.Loopback(), "h", c.devices, file)
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: err = %v, want containing %q", c.name, err, c.wantErr)
		}
	}
}

func TestPassphraseDelivery(t *testing.T) {
	cases := []struct {
		name, input, want string
		wantErr           bool
	}{
		{"match", "s3cret\ns3cret\n", "s3cret", false},
		{"retry after mismatch", "a\nb\ns3cret\ns3cret\n", "s3cret", false},
		{"retry after empty", "\n\ns3cret\ns3cret\n", "s3cret", false},
		{"no operator", "", "", true},
	}
	for _, c := range cases {
		path := filepath.Join(t.TempDir(), "disko-password")
		old := diskPasswordPath
		diskPasswordPath = path
		ui := runner.New(runner.Options{In: strings.NewReader(c.input)})
		err := askAndSendPassphrase(runner.NewContext(context.Background(), ui), pipeline.Loopback())
		diskPasswordPath = old
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: want an error", c.name)
			}
			continue
		}
		data, rerr := os.ReadFile(path)
		fi, _ := os.Stat(path)
		if err != nil || rerr != nil || string(data) != c.want || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: err=%v read=%v data=%q mode=%v", c.name, err, rerr, data, fi.Mode())
		}
	}
}
func TestParseLsblk(t *testing.T) {
	out := `loop0 1000 loop 0
sda 31457280000 disk 1
nvme0n1 512110190592 disk 0
sr0 1073741312 rom 0
sdb 1000204886016 disk 0
sdc 0 disk 0
`
	got := parseLsblk(out)
	if len(got) != 2 || got[0].Path != "/dev/nvme0n1" || got[0].Size != 512110190592 || got[1].Path != "/dev/sdb" {
		t.Fatalf("got %+v", got)
	}
	if s := got[0].String(); s != "/dev/nvme0n1 (512 GB)" {
		t.Fatalf("String() = %q", s)
	}
}

func TestChooseDisk(t *testing.T) {
	a := Disk{Path: "/dev/vda", Size: 512e9}
	b := Disk{Path: "/dev/nvme0n1", Size: 256e9}

	cases := []struct {
		name       string
		candidates []Disk
		recorded   string
		input      string
		want       string
		wantErr    string
	}{
		{"none despite recorded", nil, "/dev/recorded", "", "", "--disk"},
		{"none and nothing recorded", nil, "", "", "", "--disk"},
		{"recorded wins without asking", []Disk{a, b}, "/dev/nvme0n1", "", "/dev/nvme0n1", ""},
		{"single is taken without asking", []Disk{a}, "/dev/recorded", "", "/dev/vda", ""},
		{"several: operator picks", []Disk{a, b}, "/dev/recorded", "2\n", "/dev/nvme0n1", ""},
		{"several: bad answer", []Disk{a, b}, "/dev/recorded", "7\n", "", "--disk"},
		{"several: no answer", []Disk{a, b}, "/dev/recorded", "", "", "--disk"},
	}
	for _, c := range cases {
		ui := runner.New(runner.Options{In: strings.NewReader(c.input)})
		got, err := chooseDisk(runner.NewContext(context.Background(), ui), c.candidates, c.recorded)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: err = %v, want containing %q", c.name, err, c.wantErr)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: got %q %v, want %q", c.name, got, err, c.want)
		}
	}
}
