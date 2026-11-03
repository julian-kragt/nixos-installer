package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"installer/internal/pipeline"
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
	out := " M a b\x00R  new\x00old\x00A  c\x00"
	want := []string{"a b", "new", "old", "c"}
	if got := parseStatus(out); !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := parseStatus(""); len(got) != 0 {
		t.Fatalf("got %q", got)
	}
}

func TestPrefixAndDirty(t *testing.T) {
	dir := newRepo(t)
	m := pipeline.Machine{}
	if p, err := Prefix(ctx, m, filepath.Join(dir, "sub")); err != nil || p != "sub/" {
		t.Fatalf("prefix = %q, %v", p, err)
	}
	if _, err := Prefix(ctx, m, t.TempDir()); err == nil {
		t.Fatal("want an error outside a repository")
	}
	write(t, dir, "sub/a", "2")
	write(t, dir, "untracked", "x")
	got, err := Dirty(ctx, m, dir)
	if err != nil || !slices.Equal(got, []string{"sub/a"}) {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestHasUpstream(t *testing.T) {
	dir := newRepo(t)
	m := pipeline.Machine{}
	if ok, err := HasUpstream(ctx, m, dir); err != nil || ok {
		t.Fatalf("no remote: got %v, %v", ok, err)
	}
	remote := t.TempDir()
	sh(t, remote, "init", "-q", "--bare", "-b", "main")
	sh(t, dir, "remote", "add", "origin", remote)
	sh(t, dir, "push", "-q", "-u", "origin", "main")
	if ok, err := HasUpstream(ctx, m, dir); err != nil || !ok {
		t.Fatalf("with remote: got %v, %v", ok, err)
	}
}

func TestCommitOnlyListedPathsAndOnlyOnDiff(t *testing.T) {
	dir := newRepo(t)
	m := pipeline.Machine{}
	write(t, dir, "b", "2")
	write(t, dir, "sub/a", "2")

	done, err := Commit(ctx, m, dir, "msg", "sub/a")
	if err != nil || !done {
		t.Fatalf("first: %v, %v", done, err)
	}
	if got, _ := Dirty(ctx, m, dir); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("unlisted path was committed: dirty = %q", got)
	}
	done, err = Commit(ctx, m, dir, "msg", "sub/a")
	if err != nil || done {
		t.Fatalf("second: %v, %v; want no commit", done, err)
	}
}
