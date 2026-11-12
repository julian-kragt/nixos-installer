package secrets

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestFiles(t *testing.T) {
	got := Files("myhost", []string{"jdoe", "media"})
	want := []string{"hosts/myhost.yaml", "host-users/myhost-jdoe.yaml", "host-users/myhost-media.yaml"}
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
		{"host-users/myhost-jdoe.yaml", "jdoe", true},
		{"host-users/myhost-dev-jdoe.yaml", "", false},
		{"host-users/nexthost-jdoe.yaml", "", false},
		{"hosts/myhost.yaml", "", false},
		{"host-users/myhost-.yaml", "", false},
		{"host-users/myhost-John.yaml", "", false},
	}
	for _, tt := range tests {
		if user, ok := FileUser("myhost", tt.path); user != tt.user && tt.ok || ok != tt.ok {
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
		{"myhost", []string{"jdoe", "media_1", "_svc"}, true},
		{"myhost-dev", []string{"jdoe"}, true},
		// host-users/myhost-john-doe.yaml would also be myhost-john's file of doe.
		{"myhost", []string{"john-doe"}, false},
		// user_a_b_c would also be the anchor of a on b_c.
		{"b_c", []string{"a"}, false},
		{"myhost", []string{"John"}, false},
		{"myhost", []string{"1st"}, false},
		{"", nil, false},
		// A glob of myhost* would match other hosts' files.
		{"myhost*", nil, false},
		{"web[1]", nil, false},
		// &host_a,b is not a YAML anchor.
		{"a,b", nil, false},
		{"-myhost", nil, false},
		{"Myhost2", nil, true},
	}
	for _, tt := range tests {
		if err := CheckNames(tt.host, tt.users); (err == nil) != tt.ok {
			t.Errorf("%s %q: err = %v, want ok = %v", tt.host, tt.users, err, tt.ok)
		}
	}
}

func TestStale(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"myhost-jdoe", "myhost-gone", "myhost-dev-jdoe", "nexthost-gone"} {
		path := filepath.Join(dir, "host-users", f+".yaml")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	stale, err := Stale(dir, "myhost", []string{"jdoe"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"host-users/myhost-gone.yaml"}; !slices.Equal(stale, want) {
		t.Errorf("stale = %q, want %q", stale, want)
	}
}

func TestManaged(t *testing.T) {
	tests := []struct {
		path string
		ok   bool
	}{
		{".sops.yaml", true},
		{"hosts/myhost.yaml", true},
		{"host-users/myhost-jdoe.yaml", true},
		{"host-users/myhost-gone.yaml", true},
		{"host-users/myhost-dev-jdoe.yaml", false},
		{"hosts/nexthost.yaml", false},
		{"templates/host.yaml", false},
	}
	for _, tt := range tests {
		if ok := Managed("myhost", tt.path); ok != tt.ok {
			t.Errorf("%s: got %v, want %v", tt.path, ok, tt.ok)
		}
	}
}
