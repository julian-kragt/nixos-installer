package secrets

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// shared and both are ReferenceLayout in the other user modes, with shared
// files in users/.
var (
	shared = Layout{
		UserMode:           Shared,
		HostPath:           "hosts",
		SharedUserPath:     "users",
		HostTemplate:       "templates/host.yaml",
		SharedUserTemplate: "templates/shared-user.yaml",
		PasswordTemplate:   "templates/shared-user.yaml",
	}
	both = Layout{
		UserMode:           Both,
		HostPath:           "hosts",
		HostUserPath:       "host-users",
		SharedUserPath:     "users",
		HostTemplate:       "templates/host.yaml",
		HostUserTemplate:   "templates/user.yaml",
		SharedUserTemplate: "templates/shared-user.yaml",
		PasswordTemplate:   "templates/user.yaml",
	}
)

func TestLayoutFromJSON(t *testing.T) {
	data := `{"hostPath":"hosts","hostTemplatePath":"templates/host.yaml","hostUserPath":"host-users",` +
		`"hostUserTemplatePath":"templates/user.yaml","passwordTemplate":"templates/user.yaml",` +
		`"sharedUserPath":null,"sharedUserTemplatePath":null,"userMode":"per-host"}`
	var l Layout
	if err := json.Unmarshal([]byte(data), &l); err != nil {
		t.Fatal(err)
	}
	if l != ReferenceLayout {
		t.Errorf("layout = %+v, want %+v", l, ReferenceLayout)
	}
}

func TestLayoutCheck(t *testing.T) {
	for _, l := range []Layout{ReferenceLayout, shared, both} {
		if err := l.Check(); err != nil {
			t.Errorf("%s: %v", l.UserMode, err)
		}
	}
	broken := map[string]func(*Layout){
		"unknown mode":          func(l *Layout) { l.UserMode = "all" },
		"no host path":          func(l *Layout) { l.HostPath = "" },
		"unused shared path":    func(l *Layout) { l.SharedUserPath = "users" },
		"missing user template": func(l *Layout) { l.HostUserTemplate = "" },
		"absolute":              func(l *Layout) { l.HostPath = "/hosts" },
		"unclean":               func(l *Layout) { l.HostPath = "a/../hosts" },
		"argument syntax":       func(l *Layout) { l.HostUserTemplate = "t=1.yaml" },
		"password elsewhere":    func(l *Layout) { l.PasswordTemplate = "templates/host.yaml" },
	}
	for name, breakIt := range broken {
		l := ReferenceLayout
		breakIt(&l)
		if err := l.Check(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestFiles(t *testing.T) {
	tests := []struct {
		layout Layout
		want   []string
	}{
		{ReferenceLayout, []string{"hosts/myhost.yaml", "host-users/myhost-jdoe.yaml", "host-users/myhost-media.yaml"}},
		{shared, []string{"hosts/myhost.yaml", "users/jdoe.yaml", "users/media.yaml"}},
		{both, []string{"hosts/myhost.yaml", "host-users/myhost-jdoe.yaml", "users/jdoe.yaml", "host-users/myhost-media.yaml", "users/media.yaml"}},
	}
	for _, tt := range tests {
		if got := tt.layout.Files("myhost", []string{"jdoe", "media"}); !slices.Equal(got, tt.want) {
			t.Errorf("%s: files = %q, want %q", tt.layout.UserMode, got, tt.want)
		}
	}
}

func TestHostFilesTemplates(t *testing.T) {
	want := []File{
		{Path: "hosts/myhost.yaml", Template: "templates/host.yaml"},
		{Path: "host-users/myhost-jdoe.yaml", Template: "templates/user.yaml", User: "jdoe"},
		{Path: "users/jdoe.yaml", Template: "templates/shared-user.yaml", User: "jdoe", Shared: true},
	}
	if got := both.HostFiles("myhost", []string{"jdoe"}); !slices.Equal(got, want) {
		t.Errorf("files = %+v, want %+v", got, want)
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
		if user, ok := ReferenceLayout.FileUser("myhost", tt.path); user != tt.user && tt.ok || ok != tt.ok {
			t.Errorf("%s: got %q, %v, want %q, %v", tt.path, user, ok, tt.user, tt.ok)
		}
	}
	if _, ok := shared.FileUser("myhost", "host-users/myhost-jdoe.yaml"); ok {
		t.Error("shared layout has per-host user files")
	}
}

func TestSharedFileUser(t *testing.T) {
	tests := []struct {
		path string
		user string
		ok   bool
	}{
		{"users/jdoe.yaml", "jdoe", true},
		{"users/sub/jdoe.yaml", "", false},
		{"users/John.yaml", "", false},
		{"hosts/jdoe.yaml", "", false},
	}
	for _, tt := range tests {
		if user, ok := both.SharedFileUser(tt.path); user != tt.user || ok != tt.ok {
			t.Errorf("%s: got %q, %v, want %q, %v", tt.path, user, ok, tt.user, tt.ok)
		}
	}
	if _, ok := ReferenceLayout.SharedFileUser("users/jdoe.yaml"); ok {
		t.Error("per-host layout has shared files")
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
	for _, f := range []string{"host-users/myhost-jdoe", "host-users/myhost-gone", "host-users/myhost-dev-jdoe", "host-users/nexthost-gone", "users/gone"} {
		path := filepath.Join(dir, f+".yaml")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	for _, l := range []Layout{ReferenceLayout, both} {
		stale, err := l.Stale(dir, "myhost", []string{"jdoe"})
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"host-users/myhost-gone.yaml"}; !slices.Equal(stale, want) {
			t.Errorf("%s: stale = %q, want %q", l.UserMode, stale, want)
		}
	}
	if stale, err := shared.Stale(dir, "myhost", []string{"jdoe"}); err != nil || stale != nil {
		t.Errorf("shared: stale = %q, %v", stale, err)
	}
}

func TestManaged(t *testing.T) {
	tests := []struct {
		path        string
		ref, shared bool
	}{
		{".sops.yaml", true, true},
		{"hosts/myhost.yaml", true, true},
		{"host-users/myhost-jdoe.yaml", true, false},
		{"host-users/myhost-gone.yaml", true, false},
		{"host-users/myhost-dev-jdoe.yaml", false, false},
		{"users/jdoe.yaml", false, true},
		{"hosts/nexthost.yaml", false, false},
		{"templates/host.yaml", false, false},
	}
	for _, tt := range tests {
		if ok := ReferenceLayout.Managed("myhost", tt.path); ok != tt.ref {
			t.Errorf("per-host %s: got %v, want %v", tt.path, ok, tt.ref)
		}
		if ok := shared.Managed("myhost", tt.path); ok != tt.shared {
			t.Errorf("shared %s: got %v, want %v", tt.path, ok, tt.shared)
		}
	}
}
