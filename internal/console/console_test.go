package console

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/julian-kragt/nixos-installer/internal/machine"
)

// stubSystemd puts a systemd-run on PATH that records its args to a file and
// runs the command after --, and a systemctl that records its args. It returns
// the record file.
func stubSystemd(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	record := filepath.Join(t.TempDir(), "calls")
	runStub := `#!/bin/sh
echo "systemd-run $*" >> "` + record + `"
while [ "$1" != "--" ]; do shift; done
shift
exec "$@"
`
	ctlStub := `#!/bin/sh
echo "systemctl $*" >> "` + record + `"
`
	if err := os.WriteFile(filepath.Join(bin, "systemd-run"), []byte(runStub), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte(ctlStub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return record
}

// withHelper points helperPath and tmpDir into a temp dir, with script as the helper.
func withHelper(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	oldHelper, oldTmp := helperPath, tmpDir
	helperPath, tmpDir = filepath.Join(dir, "helper"), filepath.Join(dir, "tmp")
	t.Cleanup(func() { helperPath, tmpDir = oldHelper, oldTmp })
	if err := os.WriteFile(helperPath, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestRunStartsTheHelperOnItsTerminal(t *testing.T) {
	record := stubSystemd(t)
	out := filepath.Join(t.TempDir(), "args")
	withHelper(t, `echo "$*" > "`+out+`"`)
	s := Session{Target: machine.Loopback(), Host: "myhost"}
	if err := s.Run(context.Background(), "the test", "luks", "myhost"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(out); strings.TrimSpace(string(got)) != "luks myhost" {
		t.Errorf("helper args = %q", got)
	}
	calls, _ := os.ReadFile(record)
	for _, want := range []string{"--wait", "TTYPath=/dev/tty9", "StandardInput=tty-force", "TMPDIR=" + tmpDir, "systemctl stop " + unit} {
		if !strings.Contains(string(calls), want) {
			t.Errorf("calls lack %q:\n%s", want, calls)
		}
	}
}

func TestRunReportsAnAbortAtTheConsole(t *testing.T) {
	stubSystemd(t)
	withHelper(t, "exit 1")
	s := Session{Target: machine.Loopback(), Host: "myhost"}
	if err := s.Run(context.Background(), "the test"); !errors.Is(err, ErrAborted) {
		t.Errorf("err = %v, want ErrAborted", err)
	}
}

func TestRunStopsTheHelperWhenCancelled(t *testing.T) {
	record := stubSystemd(t)
	withHelper(t, "sleep 30")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	s := Session{Target: machine.Loopback(), Host: "myhost"}
	if err := s.Run(ctx, "the test"); err == nil || errors.Is(err, ErrAborted) {
		t.Errorf("err = %v, want the cancellation", err)
	}
	calls, _ := os.ReadFile(record)
	if n := strings.Count(string(calls), "systemctl stop "+unit); n < 2 {
		t.Errorf("systemctl stop ran %d times, want before and after:\n%s", n, calls)
	}
}

func TestWriteAndReadFile(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "a", "b", "file")
	if data, err := ReadFile(ctx, machine.Loopback(), file); err != nil || data != nil {
		t.Fatalf("missing: %q, %v", data, err)
	}
	if err := WriteFile(ctx, machine.Loopback(), file, []byte("data\n"), true); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(file); err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("not executable: %v, %v", fi.Mode(), err)
	}
	if data, err := ReadFile(ctx, machine.Loopback(), file); err != nil || string(data) != "data\n" {
		t.Errorf("read %q, %v", data, err)
	}
	if _, err := os.Stat(file + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp file left: %v", err)
	}
}

func TestHelperFromTheWrapper(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "x86_64-linux", HelperName)
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvDir, dir)
	if got, err := Helper(context.Background(), machine.Local(), "x86_64-linux"); err != nil || got != bin {
		t.Errorf("Helper = %q, %v", got, err)
	}
	if _, err := Helper(context.Background(), machine.Local(), "aarch64-linux"); err == nil {
		t.Error("want an error for a helper the wrapper lacks")
	}
	if _, err := Helper(context.Background(), machine.Local(), "x86_64-darwin"); err == nil {
		t.Error("want an error for a system without helper")
	}
}
