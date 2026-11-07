package luks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"installer/internal/machine"
	"installer/internal/runner"
)

func TestAskAndSend(t *testing.T) {
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
		old := passwordPath
		passwordPath = path
		ui := runner.New(runner.Options{In: strings.NewReader(c.input)})
		err := AskAndSend(runner.NewContext(context.Background(), ui), machine.Loopback())
		passwordPath = old
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

func TestEnsure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disko-password")
	old := passwordPath
	passwordPath = path
	defer func() { passwordPath = old }()

	// Gone, as after a reboot of the ISO: asked again.
	ui := runner.New(runner.Options{In: strings.NewReader("s3cret\ns3cret\n")})
	if err := Ensure(runner.NewContext(context.Background(), ui), machine.Loopback()); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "s3cret" {
		t.Fatalf("file = %q", data)
	}

	// Still there: nothing is asked, and there is nobody to answer.
	ui = runner.New(runner.Options{In: strings.NewReader("")})
	if err := Ensure(runner.NewContext(context.Background(), ui), machine.Loopback()); err != nil {
		t.Errorf("present: %v", err)
	}
}

func TestRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disko-password")
	old := passwordPath
	passwordPath = path
	defer func() { passwordPath = old }()

	if err := os.WriteFile(path, []byte("s3cret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if removed, err := Remove(context.Background(), machine.Loopback()); err != nil || !removed {
		t.Fatalf("present: removed = %v, %v", removed, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file still there: %v", err)
	}
	if removed, err := Remove(context.Background(), machine.Loopback()); err != nil || removed {
		t.Errorf("gone: removed = %v, %v", removed, err)
	}
}
