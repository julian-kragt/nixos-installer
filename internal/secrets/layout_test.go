package secrets

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestFiles(t *testing.T) {
	got := Files("atlas", []string{"jkragt", "media"})
	want := []string{"hosts/atlas.yaml", "host-users/atlas-jkragt.yaml", "host-users/atlas-media.yaml"}
	if !slices.Equal(got, want) {
		t.Errorf("files = %q, want %q", got, want)
	}
}

func TestFileUser(t *testing.T) {
	tests := []struct {
		path string
		user string
		ok   bool
	}{
		{"host-users/atlas-jkragt.yaml", "jkragt", true},
		{"host-users/atlas-dev-jkragt.yaml", "", false},
		{"host-users/broadway-jkragt.yaml", "", false},
		{"hosts/atlas.yaml", "", false},
		{"host-users/atlas-.yaml", "", false},
	}
	for _, tt := range tests {
		if user, ok := FileUser("atlas", tt.path); user != tt.user && tt.ok || ok != tt.ok {
			t.Errorf("%s: got %q, %v, want %q, %v", tt.path, user, ok, tt.user, tt.ok)
		}
	}
}

func TestStaleAndManaged(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"atlas-jkragt", "atlas-gone", "atlas-dev-jkragt", "broadway-gone"} {
		path := filepath.Join(dir, "host-users", f+".yaml")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	stale, err := Stale(dir, "atlas", []string{"jkragt"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"host-users/atlas-gone.yaml"}; !slices.Equal(stale, want) {
		t.Errorf("stale = %q, want %q", stale, want)
	}

	managed, err := Managed(dir, "atlas", []string{"jkragt"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".sops.yaml", "hosts/atlas.yaml", "host-users/atlas-jkragt.yaml", "host-users/atlas-gone.yaml"}
	if !slices.Equal(managed, want) {
		t.Errorf("managed = %q, want %q", managed, want)
	}
}
