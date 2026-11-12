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
		{"host-users/atlas-John.yaml", "", false},
	}
	for _, tt := range tests {
		if user, ok := FileUser("atlas", tt.path); user != tt.user && tt.ok || ok != tt.ok {
			t.Errorf("%s: got %q, %v, want %q, %v", tt.path, user, ok, tt.user, tt.ok)
		}
	}
}

func TestCheckNames(t *testing.T) {
	tests := []struct {
		host  string
		users []string
		ok    bool
	}{
		{"atlas", []string{"jkragt", "media_1", "_svc"}, true},
		{"atlas-dev", []string{"jkragt"}, true},
		// host-users/atlas-john-doe.yaml would also be atlas-john's file of doe.
		{"atlas", []string{"john-doe"}, false},
		// user_a_b_c would also be the anchor of a on b_c.
		{"b_c", []string{"a"}, false},
		{"atlas", []string{"John"}, false},
		{"atlas", []string{"1st"}, false},
		{"", nil, false},
		// A glob of atlas* would match other hosts' files.
		{"atlas*", nil, false},
		{"web[1]", nil, false},
		// &host_a,b is not a YAML anchor.
		{"a,b", nil, false},
		{"-atlas", nil, false},
		{"Atlas2", nil, true},
	}
	for _, tt := range tests {
		if err := CheckNames(tt.host, tt.users); (err == nil) != tt.ok {
			t.Errorf("%s %q: err = %v, want ok = %v", tt.host, tt.users, err, tt.ok)
		}
	}
}

func TestStale(t *testing.T) {
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
}

func TestManaged(t *testing.T) {
	tests := []struct {
		path string
		ok   bool
	}{
		{".sops.yaml", true},
		{"hosts/atlas.yaml", true},
		{"host-users/atlas-jkragt.yaml", true},
		{"host-users/atlas-gone.yaml", true},
		{"host-users/atlas-dev-jkragt.yaml", false},
		{"hosts/broadway.yaml", false},
		{"templates/host.yaml", false},
	}
	for _, tt := range tests {
		if ok := Managed("atlas", tt.path); ok != tt.ok {
			t.Errorf("%s: got %v, want %v", tt.path, ok, tt.ok)
		}
	}
}
