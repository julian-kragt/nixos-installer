package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"installer/internal/machine"
)

var ctx = context.Background()

func sh(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newRepo(t *testing.T) string {
	dir := t.TempDir()
	sh(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "sub/a", "1")
	write(t, dir, "b", "1")
	sh(t, dir, "add", ".")
	sh(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func TestParseStatus(t *testing.T) {
	out := " M a b\x00R  new\x00old\x00D  c\x00"
	want := []change{{" M", "a b"}, {"R ", "new"}, {"R ", "old"}, {"D ", "c"}}
	if got := parseStatus(out); !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := parseStatus(""); len(got) != 0 {
		t.Fatalf("got %q", got)
	}
}

func TestPrefixAndDirty(t *testing.T) {
	dir := newRepo(t)
	r := Repo{machine.Local(), dir}
	if p, err := (Repo{machine.Local(), filepath.Join(dir, "sub")}).Prefix(ctx); err != nil || p != "sub/" {
		t.Fatalf("prefix = %q, %v", p, err)
	}
	if _, err := (Repo{machine.Local(), t.TempDir()}).Prefix(ctx); err == nil {
		t.Fatal("want an error outside a repository")
	}
	write(t, dir, "sub/a", "2")
	write(t, dir, "untracked", "x")
	got, err := r.Dirty(ctx)
	if err != nil || !slices.Equal(got, []string{"sub/a"}) {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestDeleted(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "sub/c", "1")
	write(t, dir, "sub/d", "1") // keeps sub/
	sh(t, dir, "add", ".")
	sh(t, dir, "commit", "-q", "-m", "c")
	if err := os.Remove(filepath.Join(dir, "sub/a")); err != nil {
		t.Fatal(err)
	}
	sh(t, dir, "rm", "-q", "sub/c", "b")
	got, err := (Repo{machine.Local(), filepath.Join(dir, "sub")}).Deleted(ctx)
	if want := []string{"a", "c"}; err != nil || !slices.Equal(got, want) {
		t.Fatalf("got %q, %v, want %q: deletions below sub/, staged or not", got, err, want)
	}
}

// only accepts exactly paths.
func only(paths ...string) func(string) bool {
	return func(p string) bool { return slices.Contains(paths, p) }
}

// pushed gives the repository at dir an upstream.
func pushed(t *testing.T, dir string) {
	t.Helper()
	remote := t.TempDir()
	sh(t, remote, "init", "-q", "--bare", "-b", "main")
	sh(t, dir, "remote", "add", "origin", remote)
	sh(t, dir, "push", "-q", "-u", "origin", "main")
}

func TestHasUpstream(t *testing.T) {
	dir := newRepo(t)
	r := Repo{machine.Local(), dir}
	if ok, err := r.HasUpstream(ctx); err != nil || ok {
		t.Fatalf("no remote: got %v, %v", ok, err)
	}
	pushed(t, dir)
	if ok, err := r.HasUpstream(ctx); err != nil || !ok {
		t.Fatalf("with remote: got %v, %v", ok, err)
	}
}

func TestRequireCommitted(t *testing.T) {
	dir := newRepo(t)
	r := Repo{machine.Local(), filepath.Join(dir, "sub")}
	if err := (Repo{machine.Local(), t.TempDir()}).RequireCommitted(ctx, only()); err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("no repository: err = %v", err)
	}
	if err := r.RequireCommitted(ctx, only()); err == nil || !strings.Contains(err.Error(), "no upstream") {
		t.Errorf("no upstream: err = %v", err)
	}
	pushed(t, dir)
	if err := r.RequireCommitted(ctx, only()); err != nil {
		t.Errorf("clean: %v", err)
	}
	write(t, dir, "sub/a", "2") // sub/a is managed, relative to the config in sub/
	if err := r.RequireCommitted(ctx, only("a")); err != nil {
		t.Errorf("managed change: %v", err)
	}
	write(t, dir, "b", "2")
	if err := r.RequireCommitted(ctx, only("a")); err == nil || !strings.Contains(err.Error(), ": b\n") {
		t.Errorf("other change: err = %v", err)
	}
}

func TestUnmanagedChanges(t *testing.T) {
	dirty := []string{
		"cfg/flake.lock",
		"cfg/machines/h/hw.nix",
		"cfg/hosts/nixos/h/hardware-configuration.nix",
		"flake.lock",
	}
	managed := only("machines/h/hw.nix", "flake.lock")
	want := []string{"cfg/hosts/nixos/h/hardware-configuration.nix", "flake.lock"}
	if got := unmanagedChanges(dirty, "cfg/", managed); !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := unmanagedChanges(dirty[:2], "cfg/", managed); len(got) != 0 {
		t.Errorf("managed only: got %q", got)
	}
}

func TestCommitOnlyListedPathsAndOnlyOnDiff(t *testing.T) {
	dir := newRepo(t)
	r := Repo{machine.Local(), dir}
	write(t, dir, "b", "2")
	write(t, dir, "sub/a", "2")

	done, err := r.Commit(ctx, "msg", "sub/a")
	if err != nil || !done {
		t.Fatalf("first: %v, %v", done, err)
	}
	if got, _ := r.Dirty(ctx); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("unlisted path was committed: dirty = %q", got)
	}
	done, err = r.Commit(ctx, "msg", "sub/a")
	if err != nil || done {
		t.Fatalf("second: %v, %v; want no commit", done, err)
	}
}

func TestCommitADeletion(t *testing.T) {
	dir := newRepo(t)
	r := Repo{machine.Local(), dir}
	before, err := r.Head(ctx)
	if err != nil || len(before) != 40 {
		t.Fatalf("head = %q, %v", before, err)
	}
	if err := os.Remove(filepath.Join(dir, "b")); err != nil {
		t.Fatal(err)
	}
	if done, err := r.Commit(ctx, "msg", "b"); err != nil || !done {
		t.Fatalf("commit: %v, %v", done, err)
	}
	if after, _ := r.Head(ctx); after == before {
		t.Error("HEAD did not move")
	}
	if got, _ := r.Dirty(ctx); len(got) != 0 {
		t.Errorf("dirty = %q, want the deletion committed", got)
	}
}

// pushFrom makes a commit in a second clone of dir's remote, so dir falls behind.
func pushFrom(t *testing.T, dir string) {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	sh(t, other, "clone", "-q", strings.TrimSpace(string(out)), ".")
	write(t, other, "elsewhere", "1")
	sh(t, other, "add", ".")
	sh(t, other, "commit", "-q", "-m", "elsewhere")
	sh(t, other, "push", "-q")
}

func TestRequireInSync(t *testing.T) {
	t.Run("level", func(t *testing.T) {
		dir := newRepo(t)
		pushed(t, dir)
		if err := (Repo{machine.Local(), dir}).RequireInSync(ctx); err != nil {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("ahead", func(t *testing.T) {
		dir := newRepo(t)
		pushed(t, dir)
		write(t, dir, "b", "2")
		sh(t, dir, "commit", "-q", "-am", "local")
		err := Repo{machine.Local(), dir}.RequireInSync(ctx)
		if err == nil || !strings.Contains(err.Error(), "1 unpushed commit") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("behind", func(t *testing.T) {
		dir := newRepo(t)
		pushed(t, dir)
		pushFrom(t, dir)
		err := Repo{machine.Local(), dir}.RequireInSync(ctx)
		if err == nil || !strings.Contains(err.Error(), "1 commit(s) behind") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestPush(t *testing.T) {
	dir := newRepo(t)
	pushed(t, dir)
	r := Repo{machine.Local(), dir}
	write(t, dir, "b", "2")
	if done, err := r.Commit(ctx, "msg", "b"); err != nil || !done {
		t.Fatalf("commit: %v, %v", done, err)
	}

	if err := r.Push(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.RequireInSync(ctx); err != nil {
		t.Errorf("after push: %v", err)
	}
	if err := r.Push(ctx); err != nil {
		t.Errorf("push with nothing new: %v", err)
	}
}

func TestPushRejectedNamesTheRepo(t *testing.T) {
	dir := newRepo(t)
	pushed(t, dir)
	pushFrom(t, dir)
	r := Repo{machine.Local(), dir}
	write(t, dir, "b", "2")
	if _, err := r.Commit(ctx, "msg", "b"); err != nil {
		t.Fatal(err)
	}
	err := r.Push(ctx)
	if err == nil || !strings.Contains(err.Error(), dir) {
		t.Errorf("err = %v, want it to name %s", err, dir)
	}
}

func TestCheckPush(t *testing.T) {
	dir := newRepo(t)
	pushed(t, dir)
	r := Repo{machine.Local(), dir}
	if err := r.CheckPush(ctx); err != nil {
		t.Errorf("err = %v", err)
	}
	sh(t, dir, "remote", "set-url", "--push", "origin", filepath.Join(t.TempDir(), "missing"))
	if err := r.CheckPush(ctx); err == nil || !strings.Contains(err.Error(), dir) {
		t.Errorf("err = %v, want a failure naming %s", err, dir)
	}
}

func TestResetTo(t *testing.T) {
	dir := newRepo(t)
	r := Repo{machine.Local(), dir}
	base, err := r.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, "b", "2")
	if _, err := r.Commit(ctx, "msg", "b"); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "b", "3")
	write(t, dir, "sub/a", "2")

	if err := r.ResetTo(ctx, base, "b"); err != nil {
		t.Fatal(err)
	}
	if head, _ := r.Head(ctx); head != base {
		t.Errorf("HEAD = %s, want %s", head, base)
	}
	if got, _ := r.Dirty(ctx); !slices.Equal(got, []string{"sub/a"}) {
		t.Errorf("dirty = %q, want only sub/a, which was not reset", got)
	}
}

func TestSSHURL(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:owner/repo.git":       "ssh://git@github.com/owner/repo.git",
		"ssh://git@github.com/owner/repo.git": "ssh://git@github.com/owner/repo.git",
		"https://github.com/owner/repo.git":   "",
		"/srv/git/repo.git":                   "",
		"file:///srv/git/repo.git":            "",
		"github.com:owner/repo.git":           "",
	} {
		got, err := SSHURL(in)
		if want == "" && err == nil {
			t.Errorf("%s: got %q, want an error", in, got)
		}
		if want != "" && (err != nil || got != want) {
			t.Errorf("%s: got %q, %v, want %q", in, got, err, want)
		}
	}
}

func TestOriginURL(t *testing.T) {
	dir := newRepo(t)
	sh(t, dir, "remote", "add", "origin", "git@github.com:owner/repo.git")
	got, err := Repo{machine.Local(), dir}.OriginURL(ctx)
	if err != nil || got != "ssh://git@github.com/owner/repo.git" {
		t.Fatalf("got %q, %v", got, err)
	}
}
