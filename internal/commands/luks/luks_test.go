package luks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/julian-kragt/nixos-installer/internal/machine"
	"github.com/julian-kragt/nixos-installer/internal/runner"
)

// at points passwordPath into a temp dir for the test and returns it.
func at(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "disko-password")
	old := passwordPath
	passwordPath = path
	t.Cleanup(func() { passwordPath = old })
	return path
}

func TestType(t *testing.T) {
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
		t.Run(c.name, func(t *testing.T) {
			path := at(t)
			// A stale file of another mode is replaced, not reused.
			if err := os.WriteFile(path, []byte("old passphrase"), 0o644); err != nil {
				t.Fatal(err)
			}
			ui := runner.New(runner.Options{In: strings.NewReader(c.input)})
			err := Type(runner.NewContext(context.Background(), ui))
			if c.wantErr {
				if err == nil {
					t.Error("want an error")
				}
				return
			}
			data, rerr := os.ReadFile(path)
			fi, _ := os.Stat(path)
			if err != nil || rerr != nil || string(data) != c.want || fi.Mode().Perm() != 0o600 {
				t.Errorf("err=%v read=%v data=%q mode=%v", err, rerr, data, fi.Mode())
			}
		})
	}
}

func TestPresent(t *testing.T) {
	path := at(t)
	if present, err := Present(context.Background(), machine.Loopback()); err != nil || present {
		t.Fatalf("gone: present = %v, %v", present, err)
	}
	if err := os.WriteFile(path, []byte("s3cret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if present, err := Present(context.Background(), machine.Loopback()); err != nil || !present {
		t.Errorf("there: present = %v, %v", present, err)
	}
}

func TestRemove(t *testing.T) {
	path := at(t)
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
