package nixosrebuild

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/julian-kragt/nixos-installer/internal/machine"
)

// fakeSystemctl puts a systemctl on PATH whose nth call (from 1) runs the nth
// of answers, and the last one after that.
func fakeSystemctl(t *testing.T, answers ...string) {
	t.Helper()
	bin := t.TempDir()
	count := filepath.Join(bin, "count")
	script := "#!/bin/sh\n[ \"$*\" = \"is-active --quiet " + switchUnit + "\" ] || exit 2\n" +
		"n=$(( $(cat '" + count + "' 2>/dev/null || echo 0) + 1 )); echo $n > '" + count + "'\ncase $n in\n"
	for i, a := range answers[:len(answers)-1] {
		script += "  " + string(rune('1'+i)) + ") " + a + " ;;\n"
	}
	script += "  *) " + answers[len(answers)-1] + " ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
}

func fastPolls(t *testing.T) {
	old := pollInterval
	pollInterval = time.Millisecond
	t.Cleanup(func() { pollInterval = old })
}

func TestAwaitSwitch(t *testing.T) {
	fastPolls(t)
	for name, answers := range map[string][]string{
		"switch ends":       {"exit 0", "exit 0", "exit 3"},
		"target comes back": {"exit 255", "exit 0", "exit 4"},
	} {
		t.Run(name, func(t *testing.T) {
			fakeSystemctl(t, answers...)
			if err := AwaitSwitch(context.Background(), machine.Loopback(), time.Minute); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAwaitSwitchStopsAtAChangedHostKey(t *testing.T) {
	fastPolls(t)
	fakeSystemctl(t, "echo 'Host key verification failed.' >&2; exit 255")
	err := AwaitSwitch(context.Background(), machine.Loopback(), time.Minute)
	if err == nil || !strings.Contains(err.Error(), "host key changed") {
		t.Fatalf("err = %v, want a changed host key", err)
	}
}

func TestAwaitSwitchTimesOut(t *testing.T) {
	fastPolls(t)
	fakeSystemctl(t, "exit 255")
	err := AwaitSwitch(context.Background(), machine.Loopback(), 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("err = %v, want a timeout", err)
	}
}

func TestCurrentSystem(t *testing.T) {
	dir := t.TempDir()
	system := filepath.Join(dir, "nixos-system-myhost")
	if err := os.Mkdir(system, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "current-system")
	if err := os.Symlink(system, link); err != nil {
		t.Fatal(err)
	}
	old := currentSystem
	currentSystem = link
	t.Cleanup(func() { currentSystem = old })

	got, err := CurrentSystem(context.Background(), machine.Loopback())
	want, _ := filepath.EvalSymlinks(system) // macOS: /var is /private/var
	if err != nil || got != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
}
