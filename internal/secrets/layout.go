// Package secrets knows the nix-secrets repository: where a host's secret
// files are, what its .sops.yaml says about them and what a secret file must hold.
// Where the files are comes from the repository itself, as a Layout.
package secrets

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// ConfigFile is the sops configuration at the top of the repository.
const ConfigFile = ".sops.yaml"

// EntryFile declares the repository's Layout, checked by nixos-bootstrap and
// read through the repository flake's installerSecretsData output.
const EntryFile = "secrets-entry.nix"

// UserMode is how a repository keeps users' secrets.
type UserMode string

const (
	// PerHost is one file per host and user, encrypted to that host and the
	// user's key on it.
	PerHost UserMode = "per-host"
	// Shared is one file per user, encrypted to every host the user is on and
	// the user's key on each.
	Shared UserMode = "shared"
	// Both is both files for every user.
	Both UserMode = "both"
)

// Layout is where a repository keeps each kind of secret file and the template
// it follows, as its installerSecretsData output gives it. Paths are relative
// to the repository; a path that its UserMode does not use is "".
type Layout struct {
	UserMode           UserMode `json:"userMode"`
	HostPath           string   `json:"hostPath"`
	HostUserPath       string   `json:"hostUserPath"`
	SharedUserPath     string   `json:"sharedUserPath"`
	HostTemplate       string   `json:"hostTemplatePath"`
	HostUserTemplate   string   `json:"hostUserTemplatePath"`
	SharedUserTemplate string   `json:"sharedUserTemplatePath"`
	// PasswordTemplate is the user template whose files hold the login
	// password, or "".
	PasswordTemplate string `json:"passwordTemplate"`
}

// ReferenceLayout is the layout of nix-secrets-reference: per-host user files,
// as every repository had before it declared its layout.
var ReferenceLayout = Layout{
	UserMode:         PerHost,
	HostPath:         "hosts",
	HostUserPath:     "host-users",
	HostTemplate:     "templates/host.yaml",
	HostUserTemplate: "templates/user.yaml",
	PasswordTemplate: "templates/user.yaml",
}

// HasHostUser reports whether users have a file per host.
func (l Layout) HasHostUser() bool { return l.UserMode == PerHost || l.UserMode == Both }

// HasShared reports whether users have a file shared by their hosts.
func (l Layout) HasShared() bool { return l.UserMode == Shared || l.UserMode == Both }

// Check fails unless l is a layout the installer can work with. nixos-bootstrap
// checked it already; this guards against a different version of it and keeps
// paths out of the console helper's argument syntax.
func (l Layout) Check() error {
	if l.UserMode != PerHost && l.UserMode != Shared && l.UserMode != Both {
		return fmt.Errorf("nix-secrets layout: unknown userMode %q", l.UserMode)
	}
	fields := []struct {
		name, value string
		wanted      bool
	}{
		{"hostPath", l.HostPath, true},
		{"hostTemplatePath", l.HostTemplate, true},
		{"hostUserPath", l.HostUserPath, l.HasHostUser()},
		{"hostUserTemplatePath", l.HostUserTemplate, l.HasHostUser()},
		{"sharedUserPath", l.SharedUserPath, l.HasShared()},
		{"sharedUserTemplatePath", l.SharedUserTemplate, l.HasShared()},
	}
	for _, f := range fields {
		switch {
		case (f.value != "") != f.wanted:
			return fmt.Errorf("nix-secrets layout: %s is %q with userMode %q", f.name, f.value, l.UserMode)
		case strings.ContainsAny(f.value, "=: \t\n") || path.IsAbs(f.value) || f.value != path.Clean(f.value) && f.value != "":
			return fmt.Errorf("nix-secrets layout: %s %q is not a plain relative path", f.name, f.value)
		}
	}
	if p := l.PasswordTemplate; p != "" && p != l.HostUserTemplate && p != l.SharedUserTemplate {
		return fmt.Errorf("nix-secrets layout: passwordTemplate %q is not a user template", p)
	}
	return nil
}

// ErrNoEntry means the repository does not declare its layout.
var ErrNoEntry = errors.New("nix-secrets has no " + EntryFile + "; add it, with its installerSecretsData flake output (see nix-secrets-reference)")

// userName is a user name the layout keeps apart from any other: no dashes,
// so <hostUserPath>/<host>-<user>.yaml names one host and user, and no regex
// syntax, so a rule of a host whose name starts like host's is never read as
// one of host's.
var userName = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// hostName is a host name the layout keeps apart from any other: no
// underscore, so user_<user>_<host> names one host and user, and nothing a
// file glob or a YAML anchor would read as syntax.
var hostName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*$`)

// CheckNames fails unless the names of host and its users keep every file,
// rule and anchor of the layout apart: they match hostName and userName.
func CheckNames(host string, users []string) error {
	if !hostName.MatchString(host) {
		return fmt.Errorf("host %q: the nix-secrets layout needs a host name of letters, digits and -, not starting with -", host)
	}
	for _, u := range users {
		if !userName.MatchString(u) {
			return fmt.Errorf("user %q on %s: the nix-secrets layout needs a user name of lowercase letters, digits and _, not starting with a digit", u, host)
		}
	}
	return nil
}

// HostFile is the host's own secret file.
func (l Layout) HostFile(host string) string { return l.HostPath + "/" + host + ".yaml" }

// HostUserFile is the secret file of user on host. Only for HasHostUser.
func (l Layout) HostUserFile(host, user string) string {
	return l.HostUserPath + "/" + host + "-" + user + ".yaml"
}

// SharedUserFile is user's secret file on all their hosts. Only for HasShared.
func (l Layout) SharedUserFile(user string) string { return l.SharedUserPath + "/" + user + ".yaml" }

// File is a secret file of a host: its path, its template's and the user it
// belongs to, "" for the host's own file.
type File struct {
	Path, Template, User string
	Shared               bool // the user's file on all their hosts
}

// HostFiles returns the secret files host needs for users: its own first,
// then each user's, in the order given, the per-host file before the shared one.
func (l Layout) HostFiles(host string, users []string) []File {
	files := []File{{Path: l.HostFile(host), Template: l.HostTemplate}}
	for _, u := range users {
		if l.HasHostUser() {
			files = append(files, File{Path: l.HostUserFile(host, u), Template: l.HostUserTemplate, User: u})
		}
		if l.HasShared() {
			files = append(files, File{Path: l.SharedUserFile(u), Template: l.SharedUserTemplate, User: u, Shared: true})
		}
	}
	return files
}

// Files is the paths of HostFiles.
func (l Layout) Files(host string, users []string) []string {
	var paths []string
	for _, f := range l.HostFiles(host, users) {
		paths = append(paths, f.Path)
	}
	return paths
}

// Dirs is the directories of secret files, for reading them from git.
func (l Layout) Dirs() []string {
	return slices.DeleteFunc([]string{l.HostPath, l.HostUserPath, l.SharedUserPath}, func(d string) bool { return d == "" })
}

// FileUser returns the user whose per-host file on host path is, relative to
// the repository. A name with a dash after the host's is not one: it may be a
// user file of a host whose name starts like host's.
func (l Layout) FileUser(host, path string) (string, bool) {
	if !l.HasHostUser() {
		return "", false
	}
	rest, ok := strings.CutPrefix(path, l.HostUserPath+"/"+host+"-")
	if !ok {
		return "", false
	}
	return fileUser(rest)
}

// SharedFileUser returns the user whose shared file path is, relative to the
// repository.
func (l Layout) SharedFileUser(path string) (string, bool) {
	if !l.HasShared() {
		return "", false
	}
	rest, ok := strings.CutPrefix(path, l.SharedUserPath+"/")
	if !ok {
		return "", false
	}
	return fileUser(rest)
}

// fileUser is the user named by name, a user's name and ".yaml".
func fileUser(name string) (string, bool) {
	user, ok := strings.CutSuffix(name, ".yaml")
	if !ok || !userName.MatchString(user) {
		return "", false
	}
	return user, true
}

// Stale returns the per-host user files of host in the repository at dir that
// belong to none of users, relative to dir. Shared files are never stale here:
// other hosts may still use them (Applied.Emptied lists those that go).
func (l Layout) Stale(dir, host string, users []string) ([]string, error) {
	if !l.HasHostUser() {
		return nil, nil
	}
	matches, err := filepath.Glob(filepath.Join(dir, filepath.FromSlash(l.HostUserPath), host+"-*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("list user files of %s: %w", host, err)
	}
	var stale []string
	for _, m := range matches {
		rel := l.HostUserPath + "/" + filepath.Base(m)
		if user, ok := l.FileUser(host, rel); ok && !slices.Contains(users, user) {
			stale = append(stale, rel)
		}
	}
	return stale, nil
}

// Managed reports whether the secrets phase may write path, relative to the
// repository, for host: the sops configuration, the host's own file, a
// per-host user file of host, which it writes for a user on the host and
// deletes otherwise, or a shared user file, whose recipients it changes when a
// user joins or leaves the host.
func (l Layout) Managed(host, path string) bool {
	_, userFile := l.FileUser(host, path)
	_, sharedFile := l.SharedFileUser(path)
	return path == ConfigFile || path == l.HostFile(host) || userFile || sharedFile
}

// Templates is the templates the layout uses.
func (l Layout) Templates() []string {
	return slices.Compact(slices.DeleteFunc([]string{l.HostTemplate, l.HostUserTemplate, l.SharedUserTemplate}, func(t string) bool { return t == "" }))
}
