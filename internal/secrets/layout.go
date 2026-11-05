// Package secrets knows the nix-secrets repository: where a host's secret
// files are, what its .sops.yaml says about them and what a secret file must hold.
package secrets

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// ConfigFile is the sops configuration at the top of the repository.
const ConfigFile = ".sops.yaml"

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
	return user, ok && user != "" && !strings.ContainsAny(user, "-/")
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

// Managed returns every path, relative to dir, that the secrets phase writes
// for host: the sops configuration, the files of users and stale user files.
func Managed(dir, host string, users []string) ([]string, error) {
	stale, err := Stale(dir, host, users)
	if err != nil {
		return nil, err
	}
	return slices.Concat([]string{ConfigFile}, Files(host, users), stale), nil
}
