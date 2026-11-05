package hardwareconfig

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"installer/internal/commands/git"
	"installer/internal/machine"
	"installer/internal/runner"
)

const hwPath = "hosts/h/hardware-configuration.nix"

// fakeGenerator puts a nixos-generate-config on PATH with the given script body. It
// records that it ran in the returned file.
func fakeGenerator(t *testing.T, body string) string {
	t.Helper()
	bin := t.TempDir()
	ran := filepath.Join(bin, "ran")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + ran + "'\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(bin, "nixos-generate-config"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	return ran
}

func newConfig(t *testing.T) git.Repo {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "-C", dir, "init", "-q", "-b", "main")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return git.Repo{Machine: machine.Local(), Dir: dir}
}

func staged(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "diff", "--cached", "--name-only").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// operator returns a context whose runner reads input as the operator's answers, and
// the buffer the runner writes to.
func operator(input string) (context.Context, *bytes.Buffer) {
	var out bytes.Buffer
	ui := runner.New(runner.Options{Out: &out, In: strings.NewReader(input)})
	return runner.NewContext(context.Background(), ui), &out
}

func TestEnsureAsksThenGeneratesAndStagesMissingFile(t *testing.T) {
	ran := fakeGenerator(t, "echo '{ generated = true; }'")
	config := newConfig(t)
	ctx, out := operator("yes\n")

	if err := Ensure(ctx, config, machine.Loopback(), hwPath, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), filepath.Join(config.Dir, hwPath)) {
		t.Errorf("the question does not name the path: %q", out.String())
	}
	got, err := os.ReadFile(filepath.Join(config.Dir, hwPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "{ generated = true; }\n" {
		t.Errorf("file = %q", got)
	}
	if s := staged(t, config.Dir); s != hwPath {
		t.Errorf("staged = %q, want %q", s, hwPath)
	}
	args, err := os.ReadFile(ran)
	if err != nil {
		t.Fatal(err)
	}
	if string(args) != "--no-filesystems\n--show-hardware-config\n" {
		t.Errorf("args = %q", args)
	}
}

func TestEnsureApprovedDoesNotAsk(t *testing.T) {
	fakeGenerator(t, "echo generated")
	config := newConfig(t)
	ctx, out := operator("") // an unanswerable question would fail

	if err := Ensure(ctx, config, machine.Loopback(), hwPath, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Type 'yes'") {
		t.Errorf("asked although approved: %q", out.String())
	}
	if got, _ := os.ReadFile(filepath.Join(config.Dir, hwPath)); string(got) != "generated\n" {
		t.Errorf("file = %q", got)
	}
	if s := staged(t, config.Dir); s != hwPath {
		t.Errorf("staged = %q, want %q", s, hwPath)
	}
}

func TestEnsureWithoutConsentGeneratesNothing(t *testing.T) {
	for _, tt := range []struct{ name, input, want string }{
		{"refused", "no\n", "aborted"},
		{"no operator", "", "--generate-hardware"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ran := fakeGenerator(t, "echo generated")
			config := newConfig(t)
			ctx, _ := operator(tt.input)

			err := Ensure(ctx, config, machine.Loopback(), hwPath, false)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to contain %q", err, tt.want)
			}
			if _, err := os.Stat(ran); err == nil {
				t.Error("the target was asked to generate")
			}
			if _, err := os.Stat(filepath.Join(config.Dir, hwPath)); err == nil {
				t.Error("a file was written")
			}
			if s := staged(t, config.Dir); s != "" {
				t.Errorf("staged = %q", s)
			}
		})
	}
}

func TestEnsureLeavesExistingFileAlone(t *testing.T) {
	ran := fakeGenerator(t, "echo generated")
	config := newConfig(t)
	file := filepath.Join(config.Dir, hwPath)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Ensure(context.Background(), config, machine.Loopback(), hwPath, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(file); string(got) != "mine" {
		t.Errorf("file overwritten: %q", got)
	}
	if s := staged(t, config.Dir); s != hwPath {
		t.Errorf("an untracked file must be staged, or the flake cannot see it: staged = %q", s)
	}
	if _, err := os.Stat(ran); err == nil {
		t.Error("the target was asked to generate although the file exists")
	}
}

func TestEnsureRefusesPathOutsideConfig(t *testing.T) {
	for _, path := range []string{"/etc/passwd", "../outside.nix", "hosts/../../outside.nix", ""} {
		ran := fakeGenerator(t, "echo generated")
		config := newConfig(t)
		err := Ensure(context.Background(), config, machine.Loopback(), path, true)
		if err == nil || !strings.Contains(err.Error(), "not inside the config") {
			t.Errorf("%q: err = %v", path, err)
		}
		if _, err := os.Stat(ran); err == nil {
			t.Errorf("%q: the target was asked to generate", path)
		}
	}
}

func TestEnsureGeneratorFailures(t *testing.T) {
	for _, tt := range []struct{ name, body, want string }{
		{"no output", "true", "printed nothing"},
		{"blank output", "echo", "printed nothing"},
		{"command missing", "exit 127", "boot it from the NixOS ISO"},
		{"command fails", "echo broken >&2; exit 1", "broken"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fakeGenerator(t, tt.body)
			config := newConfig(t)
			ctx, _ := operator("yes\n")
			err := Ensure(ctx, config, machine.Loopback(), hwPath, false)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to contain %q", err, tt.want)
			}
			if _, statErr := os.Stat(filepath.Join(config.Dir, hwPath)); statErr == nil {
				t.Error("a file was written after the failure")
			}
		})
	}
}
