// Package git runs git on a machine, always as `git -C dir`.
package git

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"installer/internal/machine"
)

// Repo is the git repository around Dir on Machine.
type Repo struct {
	Machine machine.Machine
	Dir     string
}

func (r Repo) run(ctx context.Context, args ...string) (string, error) {
	return r.Machine.Run(ctx, "git", append([]string{"-C", r.Dir}, args...)...)
}

// Prefix is the path of Dir below the top of its repository ("" at the top,
// "sub/" below it). It fails when Dir is not in a git repository.
func (r Repo) Prefix(ctx context.Context) (string, error) {
	out, err := r.run(ctx, "rev-parse", "--show-prefix")
	return strings.TrimSpace(out), err
}

// HasUpstream reports whether the current branch tracks a remote branch.
func (r Repo) HasUpstream(ctx context.Context) (bool, error) {
	_, err := r.Machine.Quiet().Run(ctx, "git", "-C", r.Dir, "rev-parse", "--abbrev-ref", "@{upstream}")
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return false, nil
	}
	return err == nil, err
}

// change is an entry of `git status --porcelain`: its two status letters and
// a path relative to the repository top.
type change struct{ status, path string }

// changes returns the tracked files of the repository with uncommitted
// changes. Untracked files are not listed: a flake does not see them either.
func (r Repo) changes(ctx context.Context) ([]change, error) {
	out, err := r.run(ctx, "status", "--porcelain", "-z", "--untracked-files=no")
	if err != nil {
		return nil, err
	}
	return parseStatus(out), nil
}

// parseStatus returns the entries of `git status --porcelain -z` output. A
// rename or copy entry is followed by a bare entry with the original path;
// both count, with the status of the rename or copy.
func parseStatus(out string) []change {
	var changes []change
	entries := strings.Split(out, "\x00")
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		c := change{status: e[:2], path: e[3:]}
		changes = append(changes, c)
		if strings.ContainsAny(c.status, "RC") {
			i++
			if i < len(entries) && entries[i] != "" {
				changes = append(changes, change{status: c.status, path: entries[i]})
			}
		}
	}
	return changes
}

// Dirty returns the tracked files of the repository with uncommitted changes,
// as paths relative to the repository top.
func (r Repo) Dirty(ctx context.Context) ([]string, error) {
	changes, err := r.changes(ctx)
	if err != nil {
		return nil, err
	}
	paths := make([]string, len(changes))
	for i, c := range changes {
		paths[i] = c.path
	}
	return paths, nil
}

// Deleted returns the tracked files below Dir that are deleted and not
// committed, staged or not, as paths relative to Dir.
func (r Repo) Deleted(ctx context.Context) ([]string, error) {
	prefix, err := r.Prefix(ctx)
	if err != nil {
		return nil, err
	}
	changes, err := r.changes(ctx)
	if err != nil {
		return nil, err
	}
	var deleted []string
	for _, c := range changes {
		if p, ok := strings.CutPrefix(c.path, prefix); ok && strings.Contains(c.status, "D") {
			deleted = append(deleted, p)
		}
	}
	return deleted, nil
}

// RequireCommitted fails unless Dir is in a git repository whose branch has an
// upstream and that has no uncommitted changes besides the paths managed
// accepts: those (relative to Dir) that the installer changes itself.
func (r Repo) RequireCommitted(ctx context.Context, managed func(path string) bool) error {
	prefix, err := r.Prefix(ctx)
	if err != nil {
		return fmt.Errorf("%s is not a git repository: %w", r.Dir, err)
	}
	ok, err := r.HasUpstream(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("the branch of %s has no upstream; push it first (git push -u)", r.Dir)
	}
	dirty, err := r.Dirty(ctx)
	if err != nil {
		return err
	}
	if other := unmanagedChanges(dirty, prefix, managed); len(other) > 0 {
		return fmt.Errorf("uncommitted changes in %s: %s\ncommit and push them first", r.Dir, strings.Join(other, ", "))
	}
	return nil
}

// unmanagedChanges returns the dirty paths (relative to the repository top)
// that managed does not accept relative to prefix, where Dir sits in the
// repository. A path outside Dir is never managed.
func unmanagedChanges(dirty []string, prefix string, managed func(path string) bool) []string {
	var other []string
	for _, p := range dirty {
		if rel, ok := strings.CutPrefix(p, prefix); !ok || !managed(rel) {
			other = append(other, p)
		}
	}
	return other
}

// RequireInSync fetches and fails unless the current branch is level with its
// upstream. Behind, a later push would be rejected; ahead, it would publish commits
// the installer did not make.
func (r Repo) RequireInSync(ctx context.Context) error {
	if _, err := r.run(ctx, "fetch"); err != nil {
		return err
	}
	out, err := r.run(ctx, "rev-list", "--left-right", "--count", "HEAD...@{upstream}")
	if err != nil {
		return err
	}
	var ahead, behind int
	if _, err := fmt.Sscan(out, &ahead, &behind); err != nil {
		return fmt.Errorf("parse git rev-list output %q: %w", out, err)
	}
	switch {
	case behind > 0:
		return fmt.Errorf("the branch of %s is %d commit(s) behind its upstream; pull first", r.Dir, behind)
	case ahead > 0:
		return fmt.Errorf("the branch of %s has %d unpushed commit(s); push them first", r.Dir, ahead)
	}
	return nil
}

// Add stages paths (relative to Dir). A flake ignores files git does not know.
func (r Repo) Add(ctx context.Context, paths ...string) error {
	_, err := r.run(ctx, append([]string{"add", "--"}, paths...)...)
	return err
}

// Head returns the commit HEAD points at.
func (r Repo) Head(ctx context.Context) (string, error) {
	out, err := r.run(ctx, "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

// Push pushes the current branch to its upstream.
func (r Repo) Push(ctx context.Context) error {
	if _, err := r.run(ctx, "push"); err != nil {
		return fmt.Errorf("push %s: %w", r.Dir, err)
	}
	return nil
}

// Commit commits exactly paths (relative to Dir) with message, and only when
// they differ from HEAD. It reports whether a commit was made.
func (r Repo) Commit(ctx context.Context, message string, paths ...string) (bool, error) {
	if err := r.Add(ctx, paths...); err != nil {
		return false, err
	}
	_, err := r.run(ctx, append([]string{"diff", "--cached", "--quiet", "--"}, paths...)...)
	var exit *exec.ExitError
	if err == nil {
		return false, nil // nothing to commit
	}
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		return false, err
	}
	if _, err := r.run(ctx, append([]string{"commit", "-m", message, "--"}, paths...)...); err != nil {
		return false, err
	}
	return true, nil
}
