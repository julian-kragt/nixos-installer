package vim

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"installer/internal/runner"
)

// operator returns a context whose runner reads the answers to questions from
// input, and replaces vim with saves: the n-th time vim opens, the operator
// saves saves[n], or quits with :cq when it is nil. It returns the paths vim
// was opened with.
func operator(t *testing.T, input string, saves ...[]byte) (context.Context, *[]string) {
	t.Helper()
	ui := runner.New(runner.Options{Out: &bytes.Buffer{}, In: strings.NewReader(input)})
	t.Cleanup(ui.Close)
	var opened []string
	old := edit
	t.Cleanup(func() { edit = old })
	edit = func(ctx context.Context, path string) error {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if dir, _ := os.Stat(filepath.Dir(path)); info.Mode().Perm() != 0o600 || dir.Mode().Perm() != 0o700 {
			t.Errorf("file mode %o, dir mode %o; want 600 and 700", info.Mode().Perm(), dir.Mode().Perm())
		}
		n := len(opened)
		opened = append(opened, path)
		if n >= len(saves) || saves[n] == nil {
			return exec.Command("false").Run() // :cq
		}
		return os.WriteFile(path, saves[n], 0o600)
	}
	return runner.NewContext(context.Background(), ui), &opened
}

func ok([]byte) error { return nil }

// gone fails unless every directory vim was opened in is removed.
func gone(t *testing.T, opened []string) {
	t.Helper()
	for _, p := range opened {
		if _, err := os.Stat(filepath.Dir(p)); !os.IsNotExist(err) {
			t.Errorf("%s still exists: %v", filepath.Dir(p), err)
		}
	}
}

func TestSaved(t *testing.T) {
	ctx, opened := operator(t, "", []byte("a: 1\n"))
	got, err := EditUntil(ctx, "f.yaml", []byte("a: change_me\n"), ok)
	if err != nil || string(got) != "a: 1\n" {
		t.Fatalf("got %q, %v", got, err)
	}
	if filepath.Base((*opened)[0]) != "f.yaml" {
		t.Errorf("opened %s, want a file called f.yaml", (*opened)[0])
	}
	gone(t, *opened)
}

func TestQuitWithCq(t *testing.T) {
	ctx, opened := operator(t, "", nil)
	if _, err := EditUntil(ctx, "f.yaml", []byte("a: change_me\n"), ok); !errors.Is(err, ErrAborted) {
		t.Errorf("err = %v, want ErrAborted", err)
	}
	gone(t, *opened)
}

func TestUnchangedThenAbort(t *testing.T) {
	initial := []byte("a: change_me\n")
	ctx, opened := operator(t, "2\n", initial)
	if _, err := EditUntil(ctx, "f.yaml", initial, ok); !errors.Is(err, ErrAborted) {
		t.Errorf("err = %v, want ErrAborted", err)
	}
	gone(t, *opened)
}

func TestRejectedThenEditedAgain(t *testing.T) {
	var seen [][]byte
	ctx, opened := operator(t, "1\n", []byte("a: bad\n"), []byte("a: good\n"))
	old := edit
	edit = func(ctx context.Context, path string) error {
		text, _ := os.ReadFile(path)
		seen = append(seen, text)
		return old(ctx, path)
	}
	check := func(b []byte) error {
		if bytes.Contains(b, []byte("bad")) {
			return errors.New("a is wrong")
		}
		return nil
	}
	got, err := EditUntil(ctx, "f.yaml", []byte("a: change_me\n"), check)
	if err != nil || string(got) != "a: good\n" {
		t.Fatalf("got %q, %v", got, err)
	}
	if len(seen) != 2 || string(seen[1]) != "a: bad\n" {
		t.Errorf("second edit opened %q, want the rejected text", seen)
	}
	gone(t, *opened)
}

func TestCommandKeepsNothing(t *testing.T) {
	args := command(context.Background(), "/tmp/x/f.yaml", os.Stdin, os.Stdout).Args
	for _, want := range [][]string{{"--clean"}, {"-n"}, {"-i", "NONE"}, {"--cmd", "set noswapfile nobackup nowritebackup noundofile"}, {"--", "/tmp/x/f.yaml"}} {
		if !slices.ContainsFunc(windows(args, len(want)), func(w []string) bool { return slices.Equal(w, want) }) {
			t.Errorf("args %q lack %q", args, want)
		}
	}
}

func windows(s []string, n int) [][]string {
	var out [][]string
	for i := 0; i+n <= len(s); i++ {
		out = append(out, s[i:i+n])
	}
	return out
}
