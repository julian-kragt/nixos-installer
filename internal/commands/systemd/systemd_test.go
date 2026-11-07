package systemd

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"installer/internal/machine"
)

// fakeSystemctl puts a systemctl on PATH with the given script body.
func fakeSystemctl(t *testing.T, body string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
}

func TestFailedUnits(t *testing.T) {
	fakeSystemctl(t, `[ "$*" = "--failed --no-legend --plain" ] || exit 2
printf 'a.service loaded failed failed A thing\nb.mount   loaded failed failed B\n\n'`)
	got, err := FailedUnits(context.Background(), machine.Loopback())
	if err != nil || !slices.Equal(got, []string{"a.service", "b.mount"}) {
		t.Fatalf("got %q, %v", got, err)
	}

	fakeSystemctl(t, `exit 0`)
	if got, err := FailedUnits(context.Background(), machine.Loopback()); err != nil || len(got) != 0 {
		t.Fatalf("none failed: got %q, %v", got, err)
	}
}

func TestIsFailed(t *testing.T) {
	for _, tc := range []struct {
		name, body  string
		failed, err bool
	}{
		{"failed", `exit 0`, true, false},
		{"not failed", `exit 1`, false, false},
		{"no such unit", `exit 4`, false, false},
		{"connection lost", `exit 255`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeSystemctl(t, `[ "$*" = "is-failed --quiet u.service" ] || exit 2
`+tc.body)
			failed, err := IsFailed(context.Background(), machine.Loopback(), "u.service")
			if failed != tc.failed || (err != nil) != tc.err {
				t.Errorf("got %v, %v; want %v, error %v", failed, err, tc.failed, tc.err)
			}
		})
	}
}
