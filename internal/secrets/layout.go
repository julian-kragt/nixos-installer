// Package secrets knows the nix-secrets repository: where a host's secret
// files are, what its .sops.yaml says about them and what a secret file must hold.
package secrets

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// ConfigFile is the sops configuration at the top of the repository.
const ConfigFile = ".sops.yaml"

// userName is a user name the layout keeps apart from any other: no dashes,
// so host-users/<host>-<user>.yaml names one host and user, and no regex
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

// HostFile is the host's own secret file, relative to the repository.
func HostFile(host string) string { return "hosts/" + host + ".yaml" }

// UserFile is the secret file of user on host, relative to the repository.
func UserFile(host, user string) string { return "host-users/" + host + "-" + user + ".yaml" }

// Files returns the secret files host needs for users: its own first, then one
// per user in the order given.
func Files(host string, users []string) []string {
	files := []string{HostFile(host)}
	for _, u := range users {
		files = append(files, UserFile(host, u))
	}
	return files
}

// FileUser returns the user whose file on host path is, relative to the
// repository. A name with a dash after the host's is not one: it may be a user
// file of a host whose name starts like host's.
func FileUser(host, path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, "host-users/"+host+"-")
	if !ok {
		return "", false
	}
	user, ok := strings.CutSuffix(rest, ".yaml")
	return user, ok && userName.MatchString(user)
}

// RepoFiles returns the secret files in the repository at dir, hosts' and
// users', relative to dir and sorted.
func RepoFiles(dir string) ([]string, error) {
	var files []string
	for _, sub := range []string{"hosts", "host-users"} {
		matches, err := filepath.Glob(filepath.Join(dir, sub, "*.yaml"))
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", sub, err)
		}
		for _, m := range matches {
			files = append(files, sub+"/"+filepath.Base(m))
		}
	}
	slices.Sort(files)
	return files, nil
}

// Stale returns the user files of host in the repository at dir that belong
// to none of users, relative to dir.
func Stale(dir, host string, users []string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "host-users", host+"-*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("list user files of %s: %w", host, err)
	}
	var stale []string
	for _, m := range matches {
		rel := "host-users/" + filepath.Base(m)
		if user, ok := FileUser(host, rel); ok && !slices.Contains(users, user) {
			stale = append(stale, rel)
		}
	}
	return stale, nil
}

// Managed reports whether the secrets phase writes path, relative to the
// repository, for host: the sops configuration, the host's own file, or a user
// file of host, which it writes for a user on the host and deletes otherwise.
func Managed(host, path string) bool {
	_, userFile := FileUser(host, path)
	return path == ConfigFile || path == HostFile(host) || userFile
}
