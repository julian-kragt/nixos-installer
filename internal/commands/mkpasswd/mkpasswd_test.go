package mkpasswd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"installer/internal/machine"
)

func TestHashWithTheRealTool(t *testing.T) {
	if _, err := exec.LookPath("mkpasswd"); err != nil {
		t.Skip("mkpasswd not in PATH")
	}
	got, err := Hash(context.Background(), machine.Local(), "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "$y$") || strings.Contains(got, "correct horse") {
		t.Errorf("hash = %q", got)
	}
}

func TestHashPassesThePasswordOnStdin(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\ntest \"$*\" = '--method=yescrypt --stdin' || exit 9\nread -r pw\ntest \"$pw\" = 'pw 1' && echo '$y$fake'\n"
	if err := os.WriteFile(filepath.Join(bin, "mkpasswd"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	if got, err := Hash(context.Background(), machine.Local(), "pw 1"); err != nil || got != "$y$fake" {
		t.Errorf("hash = %q, %v", got, err)
	}
}
