// Package git runs git on a machine, always as `git -C dir`.
package git

import (
	"context"
	"errors"
	"os/exec"
	"strings"

	"installer/internal/pipeline"
)

// Prefix is the path of dir below the top of its repository ("" at the top,
// "sub/" below it). It fails when dir is not in a git repository.
func Prefix(ctx context.Context, m pipeline.Machine, dir string) (string, error) {
	out, err := m.Run(ctx, "git", "-C", dir, "rev-parse", "--show-prefix")
	return strings.TrimSpace(out), err
}

// HasUpstream reports whether the current branch of dir tracks a remote branch.
func HasUpstream(ctx context.Context, m pipeline.Machine, dir string) (bool, error) {
	_, err := m.Quiet().Run(ctx, "git", "-C", dir, "rev-parse", "--abbrev-ref", "@{upstream}")
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return false, nil
	}
	return err == nil, err
}

// Dirty returns the tracked files of dir's repository with uncommitted changes,
// as paths relative to the repository top. Untracked files are not listed:
// a flake does not see them either.
func Dirty(ctx context.Context, m pipeline.Machine, dir string) ([]string, error) {
	out, err := m.Run(ctx, "git", "-C", dir, "status", "--porcelain", "-z", "--untracked-files=no")
	if err != nil {
		return nil, err
	}
	return parseStatus(out), nil
}

// parseStatus returns the paths in `git status --porcelain -z` output. A
// rename or copy entry is followed by a bare entry with the original path;
// both count.
func parseStatus(out string) []string {
	var paths []string
	entries := strings.Split(out, "\x00")
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		paths = append(paths, e[3:])
		if e[0] == 'R' || e[0] == 'C' || e[1] == 'R' || e[1] == 'C' {
			i++
			if i < len(entries) && entries[i] != "" {
				paths = append(paths, entries[i])
			}
		}
	}
	return paths
}

// Add stages paths (relative to dir). A flake ignores files git does not know.
func Add(ctx context.Context, m pipeline.Machine, dir string, paths ...string) error {
	_, err := m.Run(ctx, "git", append([]string{"-C", dir, "add", "--"}, paths...)...)
	return err
}

// Commit commits exactly paths (relative to dir) with message, and only when
// they differ from HEAD. It reports whether a commit was made.
func Commit(ctx context.Context, m pipeline.Machine, dir, message string, paths ...string) (bool, error) {
	if err := Add(ctx, m, dir, paths...); err != nil {
		return false, err
	}
	diff := append([]string{"-C", dir, "diff", "--cached", "--quiet", "--"}, paths...)
	_, err := m.Run(ctx, "git", diff...)
	var exit *exec.ExitError
	if err == nil {
		return false, nil // nothing to commit
	}
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		return false, err
	}
	commit := append([]string{"-C", dir, "commit", "-m", message, "--"}, paths...)
	if _, err := m.Run(ctx, "git", commit...); err != nil {
		return false, err
	}
	return true, nil
}
